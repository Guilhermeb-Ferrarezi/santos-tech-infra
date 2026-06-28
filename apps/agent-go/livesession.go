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

	// evicted: tombstone de morte intencional (Evict por /clear, /model, /compact).
	// Quando true, o crash path do readLoop NÃO emite TURN_FAILED — a morte foi
	// planejada pelo chamador, não um crash inesperado.
	evicted bool

	watchdog *time.Timer // timer de teto por turno; armado em cada início de turno

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

// resetToIdle volta a sessão ao estado idle e zera a fila (usado quando um turno é
// abortado antes de chegar ao processo: mídia inválida ou falha de escrita no stdin).
func (ls *liveSession) resetToIdle() {
	ls.mu.Lock()
	ls.state = StatusIdle
	ls.queue = nil
	ls.mu.Unlock()
}

// persistAndWrite roda o preâmbulo de UM turno e o escreve no stdin do processo vivo.
// Espelha o RunTurn antigo (paridade de comportamento) NA MESMA ORDEM: valida/grava
// mídia, marca running, persiste a mensagem do usuário, auto-título, monta o prompt
// efetivo (nota de mídia + seed de /compact) e só então escreve no stdin. É chamada no
// começo de todo turno (1º Send e cada drenagem da fila), com state já em Running.
func (ls *liveSession) persistAndWrite(prompt string, atts []Attachment) {
	ctx := context.Background()
	m := ls.mgr
	conv := ls.conv

	// 1. Mídia: valida e grava em <workdir>/media ANTES de qualquer efeito — anexo
	// inválido aborta o turno inteiro (fail-closed) e devolve a sessão a idle.
	if err := validateAttachments(atts); err != nil {
		ls.resetToIdle()
		m.dispatch(conv.ID, turnEvent{Type: "error", Code: "BAD_ATTACHMENT", Message: err.Error()})
		return
	}
	mediaPaths, err := saveAttachments(conv.Workdir, atts)
	if err != nil {
		ls.resetToIdle()
		m.dispatch(conv.ID, turnEvent{Type: "error", Code: "BAD_ATTACHMENT", Message: err.Error()})
		return
	}

	// 2. Status running (Postgres + Redis).
	_ = m.s.setConversationStatus(ctx, conv.ID, StatusRunning)
	m.s.setState(ctx, conv.ID, StatusRunning)

	// 3. Persiste a mensagem do usuário (texto ORIGINAL + marcadores de mídia; nunca o base64).
	if err := m.s.insertMessage(ctx, &Message{
		ConversationID: conv.ID, Role: "user", Kind: "text",
		Content: map[string]any{"text": prompt + mediaMarkers(atts)},
	}); err != nil {
		slog.Warn("falha ao persistir mensagem do usuário", "conv", conv.ID, "err", err)
	}

	// 4. Auto-título no 1º turno, se ainda não tiver (sobre o prompt ORIGINAL).
	// Leitura de conv.SessionStarted e escrita de conv.Title são feitas sob ls.mu para
	// não correr com onTurnEnd, que também escreve esses campos no fim do turno.
	ls.mu.Lock()
	needTitle := (conv.Title == nil || *conv.Title == "") && !conv.SessionStarted
	var derivedTitle string
	if needTitle {
		if t := deriveTitle(prompt); t != "" {
			derivedTitle = t
			conv.Title = &derivedTitle // escrita sincronizada
		}
	}
	ls.mu.Unlock()
	if derivedTitle != "" {
		_ = m.s.setTitleIfEmpty(ctx, conv.ID, derivedTitle) // I/O fora do lock
		m.dispatch(conv.ID, turnEvent{Type: "title", Text: derivedTitle})
	}

	// 5. Prompt efetivo: nota de mídia + prompt; seed pendente (deixado por /compact) na frente.
	effective := mediaPromptNote(mediaPaths) + prompt
	if m.s.rdb != nil {
		if seed, _ := m.s.rdb.GetDel(ctx, "claude:seed:"+conv.ID).Result(); seed != "" {
			effective = seed + "\n\n---\n\n" + effective
		}
	}

	// 6. Escreve o prompt EFETIVO no stdin do processo vivo.
	if err := ls.writeUser(effective); err != nil {
		slog.Error("falha ao escrever no stdin da sessão viva", "conv", conv.ID, "err", err)
		ls.resetToIdle()
		_ = m.s.setConversationStatus(ctx, conv.ID, StatusError)
		m.s.setState(ctx, conv.ID, StatusError)
		m.dispatch(conv.ID, turnEvent{Type: "error", Code: "WRITE_FAILED", Message: err.Error()})
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

	// O processo SAIU (crash, kill ou close limpo de hibernação). Limpa o estado e
	// remove esta sessão do pool para que a PRÓXIMA mensagem a ressuscite via --resume.
	ls.mu.Lock()
	wasRunning := ls.state == StatusRunning
	evicted := ls.evicted // lê o tombstone de morte intencional
	ls.state = StatusIdle
	ls.queue = nil
	ls.mu.Unlock()
	ls.mgr.removeLive(ls.conv.ID, ls)

	// Se morreu NO MEIO de um turno (wasRunning) e NÃO foi um Evict intencional,
	// foi crash — reporta erro. Evict (ls.evicted=true) é morte planejada pelo chamador
	// (/clear, /model, /compact): ele é responsável pelo novo estado da conversa, portanto
	// não emitimos TURN_FAILED espúrio. Um close() limpo de hibernação termina com o
	// estado já idle (wasRunning=false): só remove do pool.
	if wasRunning && !evicted {
		_ = ls.mgr.s.setConversationStatus(ctx, ls.conv.ID, StatusError)
		ls.mgr.s.setState(ctx, ls.conv.ID, StatusError)
		emit(turnEvent{Type: "error", Code: "TURN_FAILED", Message: "processo encerrou inesperadamente"})
		if !ls.mgr.hasSubs(ls.conv.ID) {
			ls.mgr.s.notifyTurnDone(ctx, ls.conv, StatusError)
		}
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

// close encerra a sessão de forma limpa: fecha o stdin, o que faz o CLI sair e salvar
// a sessão no disco (base da ressurreição via --resume).
func (ls *liveSession) close() {
	if ls.stdin != nil {
		_ = ls.stdin.Close()
	}
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
	// SessionStarted e Title são lidos/escritos por persistAndWrite concorrentemente;
	// capturamos e escrevemos sob ls.mu. O I/O (markSessionStarted) é feito APÓS soltar
	// o lock — é idempotente e pode repetir sem problema.
	needMark := !ls.conv.SessionStarted
	if needMark {
		ls.conv.SessionStarted = true
	}
	titleSnap := ls.conv.Title // snapshot do *string sob o lock para notifyTurnDone
	ls.mu.Unlock()

	// Fila vazia: a conversa fica verdadeiramente ociosa. Espelha o fim de turno do
	// RunTurn antigo — status idle, marca a sessão como iniciada (habilita hibernação/
	// ressurreição via --resume) e dispara push se ninguém estiver assistindo.
	ctx := context.Background()
	_ = ls.mgr.s.setConversationStatus(ctx, ls.conv.ID, StatusIdle)
	ls.mgr.s.setState(ctx, ls.conv.ID, StatusIdle)
	if needMark {
		_ = ls.mgr.s.markSessionStarted(ctx, ls.conv.ID)
	}
	ls.mgr.dispatch(ls.conv.ID, turnEvent{Type: "done"})
	if !ls.mgr.hasSubs(ls.conv.ID) {
		// Usa snapshot de Title (lido sob ls.mu) para não correr com persistAndWrite.
		convSnap := *ls.conv
		convSnap.Title = titleSnap
		ls.mgr.s.notifyTurnDone(ctx, &convSnap, StatusIdle)
	}
}
