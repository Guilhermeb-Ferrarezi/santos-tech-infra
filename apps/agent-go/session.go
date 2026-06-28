package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// turnSlots é um semáforo GLOBAL (package-level) que limita quantos processos
// `claude` rodam ao mesmo tempo na VPS — em rajada (vários turnos disparados de
// uma vez) isso evita estourar a RAM com N processos Node pesados. Capacidade =
// CLAUDE_MAX_CONCURRENT (default 4). Inicializado em initTurnSlots (chamado no boot).
var turnSlots chan struct{}

// initTurnSlots cria o semáforo global de processos Claude. Idempotente o bastante
// para o boot: cap vem de CLAUDE_MAX_CONCURRENT (default 4, mínimo 1).
func initTurnSlots() {
	n := 4
	if v := os.Getenv("CLAUDE_MAX_CONCURRENT"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	turnSlots = make(chan struct{}, n)
}

// turnTimeout é o teto de duração de um único turno do CLI. Passado esse tempo o
// contexto é cancelado e o grupo de processos é morto (ver exec/Setpgid).
const turnTimeout = 8 * time.Minute

// turnEvent é o evento normalizado enviado ao cliente (WS) durante um turno.
type turnEvent struct {
	Type    string `json:"type"` // init|delta|tool_use|tool_result|result|error|done
	Text    string `json:"text,omitempty"`
	Name    string `json:"name,omitempty"`
	Input   any    `json:"input,omitempty"`
	Data    any    `json:"data,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

var errBusy = appErr(http.StatusConflict, "BUSY", "Já há um turno em andamento nesta conversa")

// SessionManager orquestra os processos `claude` (um por turno).
type SessionManager struct {
	s    *Server
	mu   sync.Mutex
	runs map[string]*exec.Cmd               // convID -> processo do turno atual (para interrupção)
	subs map[string]map[chan turnEvent]bool // convID -> WSs conectados (assinantes dos eventos)
	live map[string]*liveSession            // convID -> sessão viva (processo de longa duração)
}

func newSessionManager(s *Server) *SessionManager {
	return &SessionManager{s: s, runs: map[string]*exec.Cmd{}, subs: map[string]map[chan turnEvent]bool{}, live: map[string]*liveSession{}}
}

// Subscribe registra um assinante (WS) para os eventos de uma conversa. O turno roda
// independente do WS — assinar só serve para receber o stream ao vivo enquanto conectado.
func (m *SessionManager) Subscribe(convID string) (<-chan turnEvent, func()) {
	ch := make(chan turnEvent, 256)
	m.mu.Lock()
	if m.subs[convID] == nil {
		m.subs[convID] = map[chan turnEvent]bool{}
	}
	m.subs[convID][ch] = true
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs[convID], ch)
		if len(m.subs[convID]) == 0 {
			delete(m.subs, convID)
		}
		m.mu.Unlock()
		close(ch)
	}
}

func (m *SessionManager) hasSubs(convID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs[convID]) > 0
}

// dispatch envia um evento a todos os assinantes (não-bloqueante: descarta se o buffer encher).
func (m *SessionManager) dispatch(convID string, ev turnEvent) {
	m.mu.Lock()
	for ch := range m.subs[convID] {
		select {
		case ch <- ev:
		default:
		}
	}
	m.mu.Unlock()
}

// signalProcessGroup envia um sinal ao GRUPO de processos do comando (#3). Como o
// processo é iniciado com Setpgid, o pgid == pid do líder; o sinal negativo (-pgid)
// atinge o líder E todos os filhos (bash, MCP servers etc.), não só o pai.
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// Tenta o grupo (-pid); se falhar (grupo não criado), cai pro processo só.
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// killProcessGroup força SIGKILL no grupo — usado na limpeza pós-turno como rede
// de segurança contra processos órfãos.
func killProcessGroup(cmd *exec.Cmd) {
	signalProcessGroup(cmd, syscall.SIGKILL)
}

// Interrupt encerra o turno em andamento de uma conversa (SIGTERM no GRUPO — o CLI
// salva a sessão; os filhos também recebem o sinal e não viram zumbis).
func (m *SessionManager) Interrupt(convID string) bool {
	m.mu.Lock()
	cmd := m.runs[convID]
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return false
	}
	signalProcessGroup(cmd, syscall.SIGTERM)
	return true
}

// InterruptAll encerra TODOS os turnos em andamento (kill switch). Retorna quantos.
func (m *SessionManager) InterruptAll() int {
	m.mu.Lock()
	cmds := make([]*exec.Cmd, 0, len(m.runs))
	for _, c := range m.runs {
		cmds = append(cmds, c)
	}
	m.mu.Unlock()
	n := 0
	for _, c := range cmds {
		if c.Process != nil {
			// SIGTERM no GRUPO (mata o `claude` + filhos), não só o processo pai.
			signalProcessGroup(c, syscall.SIGTERM)
			n++
		}
	}
	return n
}

// deriveTitle gera um título curto a partir do 1º prompt (primeira linha, até 48 chars).
func deriveTitle(prompt string) string {
	line := strings.TrimSpace(prompt)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	line = strings.TrimLeft(line, "/#-* ")
	if r := []rune(line); len(r) > 48 {
		line = strings.TrimSpace(string(r[:48])) + "…"
	}
	return line
}

// RunTurn executa um turno completo, DESACOPLADO do WebSocket: roda em background
// (sobrevive ao app fechar / WS cair), transmite via dispatch para os assinantes e
// persiste as mensagens. Ao terminar, envia push se ninguém estiver conectado.
func (m *SessionManager) RunTurn(conv *Conversation, prompt string, atts []Attachment) {
	ctx := context.Background()
	if ok, _ := m.s.acquireTurn(ctx, conv.ID); !ok {
		m.dispatch(conv.ID, turnEvent{Type: "busy"})
		return
	}
	defer m.s.releaseTurn(ctx, conv.ID)

	emit := func(ev turnEvent) { m.dispatch(conv.ID, ev) }

	// Anexos de mídia: valida e grava em <workdir>/media ANTES de marcar o turno
	// como running — anexo inválido falha o turno inteiro (fail-closed).
	if err := validateAttachments(atts); err != nil {
		emit(turnEvent{Type: "error", Code: "BAD_ATTACHMENT", Message: err.Error()})
		emit(turnEvent{Type: "done"})
		return
	}
	mediaPaths, err := saveAttachments(conv.Workdir, atts)
	if err != nil {
		emit(turnEvent{Type: "error", Code: "BAD_ATTACHMENT", Message: err.Error()})
		emit(turnEvent{Type: "done"})
		return
	}

	_ = m.s.setConversationStatus(ctx, conv.ID, StatusRunning)
	m.s.setState(ctx, conv.ID, StatusRunning)
	_ = m.s.insertMessage(ctx, &Message{
		ConversationID: conv.ID, Role: "user", Kind: "text",
		// Marcadores no lugar do binário: o transcript nunca guarda base64.
		Content: map[string]any{"text": prompt + mediaMarkers(atts)},
	})

	// Auto-título no 1º turno, se ainda não tiver.
	if (conv.Title == nil || *conv.Title == "") && !conv.SessionStarted {
		if title := deriveTitle(prompt); title != "" {
			_ = m.s.setTitleIfEmpty(ctx, conv.ID, title)
			conv.Title = &title
			m.dispatch(conv.ID, turnEvent{Type: "title", Text: title})
		}
	}

	// Seed pendente (deixado por /compact): vira contexto da nova sessão.
	// A nota de mídia afirma ao modelo os paths dos anexos e a tool Read liberada.
	effective := mediaPromptNote(mediaPaths) + prompt
	if m.s.rdb != nil {
		if seed, _ := m.s.rdb.GetDel(ctx, "claude:seed:"+conv.ID).Result(); seed != "" {
			effective = seed + "\n\n---\n\n" + effective
		}
	}
	mediaGlob := ""
	if len(mediaPaths) > 0 {
		mediaGlob = filepath.Join(conv.Workdir, "media") + "/**"
	}

	err = m.exec(ctx, conv, effective, mediaGlob, emit)

	status := StatusIdle
	if err != nil {
		status = StatusError
		emit(turnEvent{Type: "error", Code: "TURN_FAILED", Message: err.Error()})
	}
	_ = m.s.setConversationStatus(ctx, conv.ID, status)
	m.s.setState(ctx, conv.ID, status)
	if err == nil && !conv.SessionStarted {
		_ = m.s.markSessionStarted(ctx, conv.ID)
		conv.SessionStarted = true
	}
	emit(turnEvent{Type: "done"})

	// Push só se ninguém estiver assistindo (app em background / fechado).
	if !m.hasSubs(conv.ID) {
		m.s.notifyTurnDone(ctx, conv, status)
	}
}

// RunTurnCollect roda um turno e coleta o texto do assistente (usado pelo /compact),
// agora pela sessão viva da conversa. Rejeita com errBusy se já há um turno em
// andamento — evita enfileirar o prompt de sumarização e coletar o resultado de outro
// turno como resumo (corrupção silenciosa de memória).
func (m *SessionManager) RunTurnCollect(conv *Conversation, prompt string) (string, error) {
	events, unsub := m.Subscribe(conv.ID)
	defer unsub()
	ls, err := m.ensureLive(context.Background(), conv)
	if err != nil {
		return "", err
	}
	ls.mu.Lock()
	busy := ls.state == StatusRunning
	ls.mu.Unlock()
	if busy {
		return "", errBusy
	}
	ls.Send(prompt, nil)
	var b strings.Builder
	for ev := range events {
		switch ev.Type {
		case "delta":
			b.WriteString(ev.Text)
		case "error":
			return b.String(), fmt.Errorf("%s", ev.Message)
		case "done":
			return b.String(), nil
		}
	}
	return b.String(), nil
}

// claudeArgs monta os argumentos do CLI para um turno. mediaGlob ("" = sem anexos)
// libera Read escopado ao dir de mídia da conversa quando o turno tem anexos.
func (m *SessionManager) claudeArgs(conv *Conversation, mediaGlob string) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--model", conv.Model,
	}
	if conv.Effort != "" {
		args = append(args, "--effort", conv.Effort)
	}
	if conv.ToolsDisabled {
		// Gate de processo: terceiros (whats-agent) não executam NENHUMA ferramenta,
		// mesmo que a mensagem tente induzir. Usamos uma allow-list VAZIA em vez de
		// deny-list: deny-list é frágil (lista incompleta, e o subagent Task herda
		// tudo) — allow-list vazia nega por padrão qualquer ferramenta, atual ou futura.
		//
		// IMPORTANTE: aqui NÃO passamos --dangerously-skip-permissions. Empiricamente,
		// esse flag faz o CLI ignorar a allow-list e executar ferramentas mesmo assim;
		// sem ele, em modo -p (não-interativo) toda ferramenta fora da allow-list é
		// negada automaticamente (inclusive as chamadas por um subagent). Sem --add-dir
		// e sem MCP.
		// Exceção cirúrgica: WebSearch pode ser liberado sozinho (busca pública, sem
		// URL arbitrária — diferente do WebFetch, que seria canal de exfiltração).
		// Anexos de mídia: Read escopado APENAS ao dir de mídia da conversa —
		// validado que fora do glob o modo -p nega (fail-closed); ver spec
		// 2026-06-05-whats-imagens-design.md no repo do dashboard.
		allowed := []string{}
		if conv.WebSearch {
			allowed = append(allowed, "WebSearch")
		}
		if mediaGlob != "" {
			allowed = append(allowed, "Read("+mediaGlob+")")
		}
		args = append(args, "--allowed-tools", strings.Join(allowed, ","))
	} else {
		args = append(args, "--dangerously-skip-permissions")
		args = append(args, "--add-dir", conv.Workdir)
		if mcp := m.s.mcpConfigPath(conv); fileExists(mcp) {
			args = append(args, "--mcp-config", mcp, "--strict-mcp-config")
		}
	}
	if conv.SessionStarted {
		args = append(args, "--resume", conv.SessionID)
	} else {
		args = append(args, "--session-id", conv.SessionID)
	}
	return args
}

// claudeArgsLive monta os args do modo SESSÃO VIVA: igual ao claudeArgs (que já põe
// --output-format stream-json) mais o input em streaming, para o processo ficar vivo
// lendo mensagens do stdin em vez de ler um prompt e sair.
func (m *SessionManager) claudeArgsLive(conv *Conversation, mediaGlob string) []string {
	args := m.claudeArgs(conv, mediaGlob)
	return append(args, "--input-format", "stream-json", "--replay-user-messages")
}

// claudeEnv monta o ambiente MÍNIMO e EXPLÍCITO do processo `claude`.
//
// SEGURANÇA (#1): o processo roda com --dangerously-skip-permissions, então um
// prompt injection consegue ler qualquer variável de ambiente e exfiltrá-la via
// bash/curl. Por isso NÃO usamos mais `os.Environ()` cru (que vazava TODO o
// ambiente do container) nem injetamos tokens de infra por padrão. Montamos uma
// allow-list: só as variáveis que o CLI legitimamente precisa para rodar.
//
// Removidos do env do Claude (antes vazavam por padrão):
//   - EASYPANEL_TOKEN / EASYPANEL_URL
//   - CLOUDFLARE_API_TOKEN
//   - GITHUB_TOKEN / GITHUB_PERSONAL_ACCESS_TOKEN no caso geral
//   - todo o resto de os.Environ() (qualquer segredo do container)
//
// Mantidos / passados explicitamente:
//   - CLAUDE_CODE_OAUTH_TOKEN: credencial da assinatura, sem ela o CLI não roda.
//   - HOME, PATH, LANG, TERM, TMPDIR, SHELL, USER, LOGNAME, XDG_*: essenciais para
//     o Node/CLI achar binários, config do usuário, locale, etc.
//   - GITHUB_TOKEN/GITHUB_PERSONAL_ACCESS_TOKEN: SÓ quando a conversa tem repo
//     clonado (o agente legitimamente faz git push/pull). Sem repo, fica de fora.
//
// Observação: o MCP do GitHub recebe o token no PRÓPRIO env do servidor MCP
// (writeMCPConfig em mcp.go), então não depende deste env do processo Claude.
func (m *SessionManager) claudeEnv(ctx context.Context, conv *Conversation) []string {
	env := []string{}
	// Repassa só variáveis de runtime essenciais do ambiente do container (sem segredos).
	for _, key := range []string{
		"HOME", "PATH", "LANG", "LC_ALL", "TERM", "TMPDIR", "SHELL", "USER", "LOGNAME",
		"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
		"NODE_PATH", "SSL_CERT_FILE", "SSL_CERT_DIR",
	} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	if tok, err := m.s.oauthToken(ctx); err == nil && tok != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+tok)
	}
	// GITHUB_TOKEN só quando há repo clonado nesta conversa (git push/pull legítimo).
	// Sem repo, não há motivo para o token estar no env — não o injetamos.
	if conv != nil && conv.Repo != nil && *conv.Repo != "" && m.s.cfg.GithubToken != "" {
		env = append(env, "GITHUB_TOKEN="+m.s.cfg.GithubToken,
			"GITHUB_PERSONAL_ACCESS_TOKEN="+m.s.cfg.GithubToken)
	}
	return env
}

func (m *SessionManager) exec(ctx context.Context, conv *Conversation, prompt, mediaGlob string, emit func(turnEvent)) error {
	// Semáforo global (#2): bloqueia até haver uma vaga de processo Claude livre.
	// Respeita o cancelamento do ctx (timeout do turno / shutdown) para não travar.
	select {
	case turnSlots <- struct{}{}:
		defer func() { <-turnSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}

	// Timeout do turno (#3): teto de duração; ao estourar, o ctx é cancelado e o
	// processo (e seu grupo) é morto abaixo. Deriva do ctx recebido para também
	// respeitar cancelamentos externos.
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, m.s.cfg.ClaudeBin, m.claudeArgs(conv, mediaGlob)...)
	cmd.Dir = conv.Workdir
	cmd.Env = m.claudeEnv(ctx, conv)
	cmd.Stdin = strings.NewReader(prompt)
	// (#3) Roda o CLI no PRÓPRIO grupo de processos (Setpgid): assim o kill-switch /
	// timeout consegue matar o GRUPO inteiro (o `claude` + os filhos que ele spawna,
	// ex.: bash, MCP servers) e não só o processo pai — senão filhos viram zumbis.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Se o ctx for cancelado (timeout/shutdown) e o processo não sair sozinho, o
	// exec mata após este atraso. Combinado com o kill de grupo no Interrupt.
	cmd.WaitDelay = 10 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr // logs do CLI vão pro stderr do serviço

	if err := cmd.Start(); err != nil {
		return err
	}
	m.mu.Lock()
	m.runs[conv.ID] = cmd
	m.mu.Unlock()
	// (#3) Garante que o GRUPO de processos morra quando esta função retornar (turno
	// terminou por timeout, erro ou cancelamento) — defesa contra processos órfãos.
	defer func() {
		killProcessGroup(cmd)
		m.mu.Lock()
		delete(m.runs, conv.ID)
		m.mu.Unlock()
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			slog.Warn("linha stream-json inválida", "line", string(line))
			continue
		}
		m.handleEvent(ctx, conv, ev, emit)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("claude saiu com erro: %w", err)
	}
	return sc.Err()
}

// handleEvent normaliza um evento do stream-json: transmite (emit) e persiste.
func (m *SessionManager) handleEvent(ctx context.Context, conv *Conversation, ev map[string]any, emit func(turnEvent)) {
	switch ev["type"] {
	case "system":
		emit(turnEvent{Type: "init", Data: ev})
		_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "system", Kind: "text", Content: ev})

	case "stream_event":
		// Deltas de texto ao vivo (não persistidos; o texto final vem no "assistant").
		if t := deltaText(ev); t != "" {
			emit(turnEvent{Type: "delta", Text: t})
		}

	case "assistant":
		for _, block := range messageContent(ev) {
			switch block["type"] {
			case "text":
				if t, _ := block["text"].(string); t != "" {
					_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "assistant", Kind: "text", Content: block})
				}
			case "tool_use":
				name, _ := block["name"].(string)
				emit(turnEvent{Type: "tool_use", Name: name, Input: block["input"]})
				_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "assistant", Kind: "tool_use", Content: block})
			}
		}

	case "user":
		for _, block := range messageContent(ev) {
			if block["type"] == "tool_result" {
				emit(turnEvent{Type: "tool_result", Data: block})
				_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "tool", Kind: "tool_result", Content: block})
			}
		}

	case "result":
		usage, _ := ev["usage"].(map[string]any)
		emit(turnEvent{Type: "result", Data: ev})
		_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "system", Kind: "result", Content: ev, Usage: usage})
	}
}

// ── Roteador de prompt/interrupt ─────────────────────────────────────────────

// DispatchPrompt roteia um prompt pelo tipo de conversa:
//   - ToolsDisabled=true  (WhatsApp/agentes externos): motor por-turno — spawn→roda→morre.
//     Não cria nem ocupa vaga no pool de sessões vivas.
//   - ToolsDisabled=false (admin interativo): sessão viva — ensureLive + Send.
//     Despacha SPAWN_FAILED se não houver vaga disponível.
func (m *SessionManager) DispatchPrompt(ctx context.Context, conv *Conversation, text string, atts []Attachment) {
	if conv.ToolsDisabled {
		// Por-turno: não toca m.live; processo morre ao final do turno.
		go m.RunTurn(conv, text, atts)
		return
	}
	// Sessão viva: mantém o processo ativo para interatividade.
	ls, err := m.ensureLive(ctx, conv)
	if err != nil {
		m.dispatch(conv.ID, turnEvent{Type: "error", Code: "SPAWN_FAILED", Message: err.Error()})
		return
	}
	ls.Send(text, atts)
}

// DispatchInterrupt interrompe o turno em andamento pelo tipo de conversa:
//   - ToolsDisabled=true:  SIGTERM no grupo de processos (motor por-turno).
//   - ToolsDisabled=false: control_request de interrupt na sessão viva.
func (m *SessionManager) DispatchInterrupt(conv *Conversation) {
	if conv.ToolsDisabled {
		m.Interrupt(conv.ID)
		return
	}
	m.mu.Lock()
	ls := m.live[conv.ID]
	m.mu.Unlock()
	if ls != nil {
		ls.Stop()
	}
}

// ── helpers de navegação no JSON ─────────────────────────────────────────────

func messageContent(ev map[string]any) []map[string]any {
	msg, ok := ev["message"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := msg["content"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		if bm, ok := b.(map[string]any); ok {
			out = append(out, bm)
		}
	}
	return out
}

// deltaText extrai o texto de um stream_event de content_block_delta.
func deltaText(ev map[string]any) string {
	e, ok := ev["event"].(map[string]any)
	if !ok {
		return ""
	}
	if e["type"] != "content_block_delta" {
		return ""
	}
	d, ok := e["delta"].(map[string]any)
	if !ok {
		return ""
	}
	if d["type"] == "text_delta" {
		t, _ := d["text"].(string)
		return t
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
