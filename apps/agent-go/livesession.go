package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// pendingMsg é uma mensagem aguardando na fila enquanto um turno está em andamento.
type pendingMsg struct {
	prompt string
	atts   []Attachment
}

// liveSession encapsula UM processo `claude` vivo, dedicado a uma conversa. O processo
// fica lendo mensagens do stdin (stream-json) e emitindo eventos no stdout pela sua
// vida inteira — não morre a cada turno.
type liveSession struct {
	mgr  *SessionManager
	conv *Conversation

	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu       sync.Mutex
	state    string // StatusIdle | StatusRunning
	queue    []pendingMsg
	lastUsed time.Time

	done chan struct{} // fechado quando o processo termina

	// hooks de teste (vazios em produção)
	testArgs []string
	testEnv  []string
}

func (m *SessionManager) newLiveSession(conv *Conversation) *liveSession {
	return &liveSession{
		mgr: m, conv: conv, state: StatusIdle,
		lastUsed: time.Now(), done: make(chan struct{}),
	}
}

// userMessageJSON serializa um prompt no formato de input stream-json do CLI.
// (Formato confirmado no spike — Task 1.)
func userMessageJSON(prompt string) []byte {
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": prompt}},
		},
	}
	b, _ := json.Marshal(msg)
	return b
}

// start faz spawn do processo vivo e dispara o reader. O grupo de processos (Setpgid)
// permite matar o claude + filhos juntos.
func (ls *liveSession) start(ctx context.Context) error {
	args := ls.testArgs
	if args == nil {
		args = ls.mgr.claudeArgsLive(ls.conv, "")
	}
	cmd := exec.Command(ls.mgr.s.cfg.ClaudeBin, args...)
	cmd.Dir = ls.conv.Workdir
	if ls.testEnv != nil {
		cmd.Env = ls.testEnv
	} else {
		cmd.Env = ls.mgr.claudeEnv(ctx, ls.conv)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	ls.cmd = cmd
	ls.stdin = stdin
	go ls.readLoop(ctx, stdout)
	return nil
}

// Send envia um prompt ao processo vivo. Se um turno já está em andamento, enfileira.
func (ls *liveSession) Send(prompt string, atts []Attachment) {
	ls.mu.Lock()
	ls.lastUsed = time.Now()
	if ls.state == StatusRunning {
		ls.queue = append(ls.queue, pendingMsg{prompt, atts})
		ls.mu.Unlock()
		ls.mgr.dispatch(ls.conv.ID, turnEvent{Type: "queued", Text: prompt})
		return
	}
	ls.state = StatusRunning
	ls.mu.Unlock()
	ls.persistAndWrite(prompt, atts)
}

// persistAndWrite grava a mensagem do usuário no transcript e a escreve no stdin do
// processo (a ordem garante transcript correto mesmo com fila).
func (ls *liveSession) persistAndWrite(prompt string, atts []Attachment) {
	if err := ls.mgr.s.insertMessage(context.Background(), &Message{
		ConversationID: ls.conv.ID, Role: "user", Kind: "text",
		Content: map[string]any{"text": prompt + mediaMarkers(atts)},
	}); err != nil {
		slog.Warn("falha ao persistir mensagem do usuário", "conv", ls.conv.ID, "err", err)
	}
	if err := ls.writeUser(prompt); err != nil {
		slog.Error("falha ao escrever no stdin da sessão viva", "conv", ls.conv.ID, "err", err)
		ls.mu.Lock()
		ls.state = StatusIdle
		ls.queue = nil
		ls.mu.Unlock()
		ls.mgr.dispatch(ls.conv.ID, turnEvent{Type: "error", Code: "WRITE_FAILED", Message: err.Error()})
	}
}

func (ls *liveSession) writeUser(prompt string) error {
	line := append(userMessageJSON(prompt), '\n')
	_, err := ls.stdin.Write(line)
	return err
}

// readLoop lê o stdout do processo pela vida inteira: normaliza/persiste/transmite cada
// evento (reusa handleEvent) e, ao ver "result" (fim de turno), volta a idle e drena a fila.
func (ls *liveSession) readLoop(ctx context.Context, stdout io.Reader) {
	defer close(ls.done)
	emit := func(ev turnEvent) { ls.mgr.dispatch(ls.conv.ID, ev) }
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		ls.mgr.handleEvent(ctx, ls.conv, ev, emit)
		if ev["type"] == "result" {
			ls.onTurnEnd()
		}
	}
	if err := ls.cmd.Wait(); err != nil {
		slog.Debug("processo da sessão viva encerrado", "conv", ls.conv.ID, "err", err)
	}
}

// Stop interrompe o TURNO atual sem matar o processo: envia um control_request de
// interrupt pelo stdin. O CLI encerra o turno e emite um "result"; o readLoop então
// volta a sessão para idle. (Mecanismo confirmado no spike — Task 1.)
func (ls *liveSession) Stop() {
	ls.mu.Lock()
	running := ls.state == StatusRunning
	ls.queue = nil // descarta a fila ao parar
	ls.mu.Unlock()
	if !running || ls.stdin == nil {
		return
	}
	_, _ = ls.stdin.Write([]byte(`{"type":"control_request","request":{"subtype":"interrupt"}}` + "\n"))
}

// onTurnEnd marca o fim de um turno: volta a idle e, se houver fila, dispara a próxima.
func (ls *liveSession) onTurnEnd() {
	ls.mu.Lock()
	if len(ls.queue) > 0 {
		next := ls.queue[0]
		ls.queue = ls.queue[1:]
		ls.lastUsed = time.Now()
		ls.mu.Unlock()
		ls.persistAndWrite(next.prompt, next.atts) // continua running
		return
	}
	ls.state = StatusIdle
	ls.mu.Unlock()
	ls.mgr.dispatch(ls.conv.ID, turnEvent{Type: "done"})
}
