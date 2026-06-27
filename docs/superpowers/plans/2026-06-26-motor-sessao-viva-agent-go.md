# Motor de Sessão Viva (agent-go) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Trocar o motor spawn-por-turno do `agent-go` por um motor de sessão viva — um processo `claude` de longa duração por conversa, alimentado por input em streaming — destravando conversa contínua sem cold start e interação enquanto o agente trabalha.

**Architecture:** Cada conversa ativa tem um `liveSession` que encapsula um processo `claude -p --input-format stream-json` vivo. Mensagens são escritas no stdin do processo; eventos saem pelo stdout e são transmitidos via o `Subscribe`/`dispatch` que já existe. Sessões ociosas hibernam (processo morto) e ressuscitam via `--resume`. Tudo em memória, num pool com cap, no container atual.

**Tech Stack:** Go (stdlib `os/exec`, `net/http`, `encoding/json`), `coder/websocket`, `go-redis/v9`, `pgx/v5`. CLI `claude` 2.1.195.

## Global Constraints

- **Pré-commit obrigatório** (de `apps/agent-go/CLAUDE.md`): `gofmt -l .` (saída vazia) · `go vet ./...` · `go build ./...` · `go test ./...`. Rodar antes de cada commit; corrigir antes de prosseguir.
- **Sem novas dependências externas.** O *fake claude* dos testes usa o idioma `TestHelperProcess` da stdlib.
- **Protocolo WS retrocompatível.** Cliente continua mandando `{type:"prompt"|"interrupt"}`; eventos atuais (`init`,`delta`,`tool_use`,`tool_result`,`result`,`error`,`busy`,`done`,`title`) seguem iguais. `agent-mobile` não pode quebrar.
- **Escala uso interno**, pool em memória, single-instance. Cap de sessões vivas: `CLAUDE_MAX_LIVE` (default `4`). TTL de hibernação: `CLAUDE_IDLE_TTL` (default `15m`).
- **Comando do motor vivo:** `claude -p --input-format stream-json --output-format stream-json --include-partial-messages --replay-user-messages --dangerously-skip-permissions --model <m> --add-dir <wd> [--mcp-config … --strict-mcp-config] (--session-id|--resume) <sid>`.
- Todo arquivo Go novo é `package main` no diretório `apps/agent-go`.
- Idioma do código/comentários/commits: português, sem `Co-Authored-By`.

---

## Task 1: Spike — confirmar o protocolo stream-json (input + interrupt)

Investigação manual contra o CLI real **antes** de escrever o motor. Os resultados ajustam as Tasks 4 e 5. Não há código de produção aqui — o entregável é um doc com respostas.

**Files:**
- Create: `apps/agent-go/docs/spike-stream-json.md` (anotações)

- [ ] **Step 1: Confirmar que o processo fica vivo lendo stdin**

Run:
```bash
cd apps/agent-go
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"diga apenas OK"}]}}' \
  | claude -p --input-format stream-json --output-format stream-json \
      --include-partial-messages --replay-user-messages \
      --model sonnet 2>/tmp/claude-stderr.log
```
Expected: sai JSONL no stdout com um evento `{"type":"system",...}` (init), depois `assistant`, depois `{"type":"result",...}`. Anotar o **formato exato** do objeto de input que foi aceito (campo `type`, estrutura de `message.content`).

- [ ] **Step 2: Confirmar multi-turno num processo vivo (sem flag entre turnos)**

Run (mantém o stdin aberto com duas mensagens, uma por linha):
```bash
{ printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"meu nome é Gui"}]}}';
  sleep 8;
  printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"qual meu nome?"}]}}';
  sleep 8; } \
  | claude -p --input-format stream-json --output-format stream-json --model sonnet
```
Expected: dois blocos de eventos, cada um terminando em `result`; a 2ª resposta menciona "Gui" (prova memória em processo). Anotar se aparece **um `system`/init só no começo** ou **um por turno**.

- [ ] **Step 3: Descobrir como interromper um turno SEM matar o processo**

Tentar enviar um control-request pelo stdin durante um turno longo:
```bash
{ printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"conte de 1 a 200 devagar, um número por linha"}]}}';
  sleep 2;
  printf '%s\n' '{"type":"control_request","request":{"subtype":"interrupt"}}';
  sleep 3; } \
  | claude -p --input-format stream-json --output-format stream-json --model sonnet
```
Expected — anotar **qual** dos casos ocorre:
- (A) a contagem para e o processo continua aceitando input → **interrupt limpo existe**, usar na Task 5.
- (B) erro / nada acontece → **não há interrupt limpo**; Task 5 usa o fallback *matar processo + ressuscitar via `--resume`*.
Anotar o `subtype` correto se o formato acima for rejeitado (testar também `{"type":"control_request","request":{"subtype":"interrupt"},"request_id":"r1"}`).

- [ ] **Step 4: Confirmar que fechar o stdin salva a sessão (base da ressurreição)**

Run:
```bash
SID=$(uuidgen)
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"lembre: a cor é roxo"}]}}' \
  | claude -p --input-format stream-json --output-format stream-json --session-id "$SID" --model sonnet >/dev/null
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"qual cor eu pedi pra lembrar?"}]}}' \
  | claude -p --input-format stream-json --output-format stream-json --resume "$SID" --model sonnet
```
Expected: a 2ª invocação (processo novo, `--resume`) responde "roxo". Confirma que fechar o stdin persiste a sessão no disco. Se falhar, anotar — a hibernação precisará de outro gatilho de flush.

- [ ] **Step 5: Registrar os achados**

Escrever em `apps/agent-go/docs/spike-stream-json.md`: formato exato da user message (Step 1), comportamento de init por turno (Step 2), mecanismo de interrupt escolhido A ou B (Step 3), confirmação da ressurreição (Step 4). Commitar.

```bash
git add apps/agent-go/docs/spike-stream-json.md
git commit -m "docs(agent-go): spike do protocolo stream-json (input/interrupt)"
```

> **Gate:** Tasks 4 e 5 assumem o formato `{"type":"user","message":{"role":"user","content":[{"type":"text","text":...}]}}` e interrupt **caso A**. Se o spike contradisser, ajuste `userMessageJSON` (Task 4) e `Stop` (Task 5) conforme anotado.

---

## Task 2: `claudeArgsLive` — flags do modo streaming

Adiciona os flags de input streaming reaproveitando o `claudeArgs` atual, sem tocar nas chamadas existentes.

**Files:**
- Modify: `apps/agent-go/session.go` (adicionar método após `claudeArgs`, ~linha 318)
- Test: `apps/agent-go/session_test.go`

**Interfaces:**
- Consumes: `(m *SessionManager) claudeArgs(conv *Conversation, mediaGlob string) []string` (existente).
- Produces: `(m *SessionManager) claudeArgsLive(conv *Conversation, mediaGlob string) []string` — mesmos args de `claudeArgs` mais `--input-format stream-json` e `--replay-user-messages`.

- [ ] **Step 1: Escrever o teste que falha**

Em `session_test.go`:
```go
func TestClaudeArgsLiveAdicionaInputStreaming(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: "/tmp/agent-test/c1"}
	args := m.claudeArgsLive(conv, "")

	assertPairValue(t, args, "--input-format", "stream-json")
	assertPairValue(t, args, "--output-format", "stream-json")
	if !slices.Contains(args, "--replay-user-messages") {
		t.Fatalf("modo live deveria passar --replay-user-messages: %v", args)
	}
	// herda o comportamento de sessão do claudeArgs
	assertPairValue(t, args, "--session-id", "s1")
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/agent-go && go test ./... -run TestClaudeArgsLive -v`
Expected: FAIL — `m.claudeArgsLive undefined`.

- [ ] **Step 3: Implementar**

Em `session.go`, após o fim de `claudeArgs`:
```go
// claudeArgsLive monta os args do modo SESSÃO VIVA: igual ao claudeArgs (que já põe
// --output-format stream-json) mais o input em streaming, para o processo ficar vivo
// lendo mensagens do stdin em vez de ler um prompt e sair.
func (m *SessionManager) claudeArgsLive(conv *Conversation, mediaGlob string) []string {
	args := m.claudeArgs(conv, mediaGlob)
	return append(args, "--input-format", "stream-json", "--replay-user-messages")
}
```

- [ ] **Step 4: Rodar e ver passar**

Run: `cd apps/agent-go && go test ./... -run TestClaudeArgsLive -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd apps/agent-go && gofmt -w . && go vet ./... && go build ./... && go test ./...
git add session.go session_test.go
git commit -m "feat(agent-go): claudeArgsLive com input streaming"
```

---

## Task 3: Infra de teste — *fake claude* via `TestHelperProcess`

Cria um binário falso que finge ser o CLI `claude` em modo stream-json, para testar o motor sem o CLI real, Postgres ou Redis. Usado por todas as tasks seguintes.

**Files:**
- Create: `apps/agent-go/livesession_test.go`

**Interfaces:**
- Produces:
  - `func fakeClaudeBin(t *testing.T) string` — retorna o caminho do binário de teste configurado para rodar como fake claude.
  - `func TestHelperProcess(t *testing.T)` — o "programa" fake: ativado por `GO_WANT_FAKE_CLAUDE=1`.
  - Variáveis de ambiente que moldam o fake: `FAKE_DELAY_MS` (atraso antes de emitir `result`), `FAKE_EMIT_INIT` (`1` emite um `system` antes de cada resposta).

- [ ] **Step 1: Escrever o fake e um teste que o exercita diretamente**

Em `livesession_test.go`:
```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// fakeClaudeBin devolve um "claude" falso: re-executa o binário de teste com
// GO_WANT_FAKE_CLAUDE=1, fazendo-o entrar em TestHelperProcess.
func fakeClaudeBin(t *testing.T) string {
	t.Helper()
	return os.Args[0]
}

// fakeEnv monta o ambiente que ativa o fake e o configura.
func fakeEnv(extra ...string) []string {
	env := append(os.Environ(), "GO_WANT_FAKE_CLAUDE=1")
	return append(env, extra...)
}

// TestHelperProcess NÃO é um teste de verdade: quando GO_WANT_FAKE_CLAUDE=1 está
// setado, age como o CLI claude em modo stream-json — lê mensagens JSON do stdin
// (uma por linha) e, para cada uma, emite eventos stream-json no stdout.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FAKE_CLAUDE") != "1" {
		return
	}
	delay := 0
	if v, err := strconv.Atoi(os.Getenv("FAKE_DELAY_MS")); err == nil {
		delay = v
	}
	emitInit := os.Getenv("FAKE_EMIT_INIT") == "1"
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	turn := 0
	for sc.Scan() {
		line := sc.Bytes()
		var in map[string]any
		if json.Unmarshal(line, &in) != nil {
			continue
		}
		if in["type"] != "user" {
			continue // ignora control_request etc. no fake base
		}
		turn++
		if emitInit {
			fmt.Fprintln(out, `{"type":"system","subtype":"init"}`)
		}
		fmt.Fprintf(out, `{"type":"assistant","message":{"content":[{"type":"text","text":"resposta %d"}]}}`+"\n", turn)
		out.Flush()
		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		fmt.Fprintf(out, `{"type":"result","subtype":"success","turn":%d}`+"\n", turn)
		out.Flush()
	}
	os.Exit(0)
}

func TestFakeClaudeRespondeStreamJSON(t *testing.T) {
	cmd := exec.Command(fakeClaudeBin(t), "-test.run=TestHelperProcess")
	cmd.Env = fakeEnv()
	cmd.Stdin = strings.NewReader(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"oi"}]}}` + "\n")
	outBytes, err := cmd.Output()
	if err != nil {
		t.Fatalf("fake claude falhou: %v", err)
	}
	got := string(outBytes)
	if !strings.Contains(got, `"type":"assistant"`) || !strings.Contains(got, `"type":"result"`) {
		t.Fatalf("saída do fake não tem assistant+result: %q", got)
	}
}
```
Adicionar os imports `"strings"` ao arquivo.

- [ ] **Step 2: Rodar e ver passar**

Run: `cd apps/agent-go && go test ./... -run 'TestFakeClaude|TestHelperProcess' -v`
Expected: `TestFakeClaudeRespondeStreamJSON` PASS; `TestHelperProcess` aparece como PASS (no-op fora do modo fake).

- [ ] **Step 3: Commit**

```bash
cd apps/agent-go && gofmt -w . && go vet ./... && go build ./... && go test ./...
git add livesession_test.go
git commit -m "test(agent-go): fake claude via TestHelperProcess"
```

---

## Task 4: `liveSession` — spawn, leitura contínua, `Send`, fila

O coração do motor. Um processo vivo por conversa, com leitura contínua do stdout (reusa `handleEvent`/`dispatch`), `Send` que enfileira quando ocupado, e drenagem da fila ao fim de cada turno.

**Files:**
- Create: `apps/agent-go/livesession.go`
- Test: `apps/agent-go/livesession_test.go` (adicionar)

**Interfaces:**
- Consumes: `m.claudeArgsLive`, `m.claudeEnv`, `m.handleEvent`, `m.dispatch`, `m.s.insertMessage`, `StatusIdle`/`StatusRunning`.
- Produces:
  - `type liveSession struct{ ... }` com campos `mgr *SessionManager`, `conv *Conversation`, `cmd *exec.Cmd`, `stdin io.WriteCloser`, `mu sync.Mutex`, `state string`, `queue []pendingMsg`, `lastUsed time.Time`, `done chan struct{}`.
  - `type pendingMsg struct{ prompt string; atts []Attachment }`
  - `func userMessageJSON(prompt string) []byte`
  - `func (m *SessionManager) newLiveSession(conv *Conversation) *liveSession`
  - `func (ls *liveSession) start(ctx context.Context) error`
  - `func (ls *liveSession) Send(prompt string, atts []Attachment)`
  - `func (ls *liveSession) writeUser(prompt string) error` (privado)
  - `func (ls *liveSession) readLoop(ctx context.Context)` (privado)

- [ ] **Step 1: Escrever o teste de sessão viva (multi-turno num processo) e de fila**

Em `livesession_test.go`:
```go
// liveTestManager cria um SessionManager apontando o ClaudeBin para o fake.
func liveTestManager(t *testing.T) *SessionManager {
	t.Helper()
	s := &Server{cfg: Config{WorkspaceRoot: t.TempDir(), ClaudeBin: fakeClaudeBin(t), DefaultModel: "sonnet"}}
	return newSessionManager(s)
}

func TestLiveSessionDoisTurnosMesmoProcesso(t *testing.T) {
	m := liveTestManager(t)
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: t.TempDir(), ToolsDisabled: true}
	ls := m.newLiveSession(conv)
	// força o fake em vez do CLI real e marca os args de teste
	ls.testArgs = []string{"-test.run=TestHelperProcess"}
	ls.testEnv = fakeEnv()

	events, unsub := m.Subscribe(conv.ID)
	defer unsub()
	if err := ls.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	ls.Send("primeira", nil)
	ls.Send("segunda", nil) // deve enfileirar (1º turno ocupa) e rodar depois

	results := collectResults(t, events, 2)
	if len(results) != 2 {
		t.Fatalf("esperava 2 results, veio %d", len(results))
	}
}

// collectResults lê eventos até ver n eventos do tipo "result" (ou estourar timeout).
func collectResults(t *testing.T, events <-chan turnEvent, n int) []turnEvent {
	t.Helper()
	var out []turnEvent
	timeout := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case ev := <-events:
			if ev.Type == "result" {
				out = append(out, ev)
			}
		case <-timeout:
			return out
		}
	}
	return out
}

func TestUserMessageJSONFormato(t *testing.T) {
	b := userMessageJSON("oi")
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json inválido: %v", err)
	}
	if m["type"] != "user" {
		t.Fatalf("type errado: %v", m["type"])
	}
}
```
Adicionar import `"context"`.

> Os campos `testArgs`/`testEnv` no `liveSession` são *hooks de teste*: quando setados, `start` os usa em vez de `claudeArgsLive`/`claudeEnv`. Mantém o teste hermético sem mexer no caminho de produção.

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/agent-go && go test ./... -run 'TestLiveSession|TestUserMessageJSON' -v`
Expected: FAIL — `m.newLiveSession undefined`, `userMessageJSON undefined`.

- [ ] **Step 3: Implementar `livesession.go`**

```go
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
	_ = ls.mgr.s.insertMessage(context.Background(), &Message{
		ConversationID: ls.conv.ID, Role: "user", Kind: "text",
		Content: map[string]any{"text": prompt + mediaMarkers(atts)},
	})
	if err := ls.writeUser(prompt); err != nil {
		slog.Error("falha ao escrever no stdin da sessão viva", "conv", ls.conv.ID, "err", err)
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
```

- [ ] **Step 4: Rodar e ver passar**

Run: `cd apps/agent-go && go test ./... -run 'TestLiveSession|TestUserMessageJSON' -v`
Expected: PASS — recebe 2 `result`, provando processo vivo + fila funcionando.

- [ ] **Step 5: Commit**

```bash
cd apps/agent-go && gofmt -w . && go vet ./... && go build ./... && go test ./...
git add livesession.go livesession_test.go
git commit -m "feat(agent-go): liveSession com leitura contínua, Send e fila"
```

---

## Task 5: `Stop` — interromper o turno sem matar o processo

Implementa o "botão parar". Usa o mecanismo confirmado no spike (Task 1). Este plano assume o **caso A** (control_request); se o spike achou o **caso B**, troque o corpo de `Stop` pelo fallback indicado no Step 3.

**Files:**
- Modify: `apps/agent-go/livesession.go`
- Test: `apps/agent-go/livesession_test.go`

**Interfaces:**
- Produces: `func (ls *liveSession) Stop()`.

- [ ] **Step 1: Estender o fake para reagir ao interrupt**

Em `TestHelperProcess` (Task 3), antes do `if in["type"] != "user"`, tratar control_request:
```go
		if in["type"] == "control_request" {
			fmt.Fprintln(out, `{"type":"result","subtype":"interrupted"}`)
			out.Flush()
			continue
		}
```

- [ ] **Step 2: Escrever o teste de Stop (processo sobrevive)**

Em `livesession_test.go`:
```go
func TestLiveSessionStopMantemProcessoVivo(t *testing.T) {
	m := liveTestManager(t)
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: t.TempDir(), ToolsDisabled: true}
	ls := m.newLiveSession(conv)
	ls.testArgs = []string{"-test.run=TestHelperProcess"}
	ls.testEnv = fakeEnv("FAKE_DELAY_MS=400")

	events, unsub := m.Subscribe(conv.ID)
	defer unsub()
	if err := ls.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	ls.Send("turno longo", nil)
	time.Sleep(50 * time.Millisecond)
	ls.Stop()
	_ = collectResults(t, events, 1) // result (interrupted) chega

	// processo deve continuar vivo: um novo Send produz outro result
	ls.Send("depois do stop", nil)
	if got := collectResults(t, events, 1); len(got) != 1 {
		t.Fatalf("processo deveria seguir vivo após Stop; results=%d", len(got))
	}
	select {
	case <-ls.done:
		t.Fatalf("processo morreu após Stop — não deveria")
	default:
	}
}
```

- [ ] **Step 3: Implementar `Stop` (caso A — control_request)**

Em `livesession.go`:
```go
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
```
> **Se o spike achou o caso B** (sem interrupt limpo), substitua o corpo por: matar o grupo de processos (`signalProcessGroup(ls.cmd, syscall.SIGTERM)`), aguardar `<-ls.done`, e deixar a sessão fora do pool — a próxima mensagem ressuscita via `--resume` (Task 6 já faz isso). Ajuste o teste para esperar `ls.done` fechar.

- [ ] **Step 4: Rodar e ver passar**

Run: `cd apps/agent-go && go test ./... -run TestLiveSessionStop -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd apps/agent-go && gofmt -w . && go vet ./... && go build ./... && go test ./...
git add livesession.go livesession_test.go
git commit -m "feat(agent-go): liveSession.Stop interrompe turno sem matar processo"
```

---

## Task 6: Pool de sessões — `ensureLive`, cap e evict LRU

Gerencia o conjunto de sessões vivas no `SessionManager`: cria/reusa por conversa, respeita o cap, e hiberna a LRU ociosa quando cheio. `ensureLive` usa `--session-id` para conversa nova e `--resume` para ressuscitar (o `claudeArgsLive` já decide isso via `conv.SessionStarted`).

**Files:**
- Modify: `apps/agent-go/session.go` (campo no `SessionManager`, ~linha 56; init em `newSessionManager`, ~linha 63)
- Modify: `apps/agent-go/config.go` (ler `CLAUDE_MAX_LIVE`)
- Create: `apps/agent-go/livepool.go`
- Test: `apps/agent-go/livesession_test.go`

**Interfaces:**
- Consumes: `m.newLiveSession`, `ls.start`, `ls.Send`, `ls.close` (Task 7 define `close`; aqui basta `evict` matar via grupo).
- Produces:
  - Campo `live map[string]*liveSession` em `SessionManager`.
  - `func (m *SessionManager) ensureLive(ctx context.Context, conv *Conversation) (*liveSession, error)`
  - `func (m *SessionManager) Evict(convID string)`
  - `func (m *SessionManager) maxLive() int`

- [ ] **Step 1: Adicionar o campo `live` ao SessionManager**

Em `session.go`, na struct `SessionManager` (após `subs`):
```go
	live map[string]*liveSession // convID -> sessão viva (processo de longa duração)
```
Em `newSessionManager`:
```go
	return &SessionManager{s: s, runs: map[string]*exec.Cmd{}, subs: map[string]map[chan turnEvent]bool{}, live: map[string]*liveSession{}}
```

- [ ] **Step 2: Escrever o teste de ensureLive + cap/evict**

Em `livesession_test.go`:
```go
func TestEnsureLiveReusaMesmaConversa(t *testing.T) {
	m := liveTestManager(t)
	m.s.cfg.MaxLive = 4
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: t.TempDir(), ToolsDisabled: true}
	a, err := m.ensureLive(context.Background(), conv)
	if err != nil {
		t.Fatalf("ensureLive: %v", err)
	}
	b, _ := m.ensureLive(context.Background(), conv)
	if a != b {
		t.Fatalf("ensureLive deveria reusar a sessão viva da mesma conversa")
	}
}

func TestEnsureLiveEvictaLRUQuandoCheio(t *testing.T) {
	m := liveTestManager(t)
	m.s.cfg.MaxLive = 1
	c1 := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: t.TempDir(), ToolsDisabled: true}
	c2 := &Conversation{ID: "c2", SessionID: "s2", Model: "sonnet", Workdir: t.TempDir(), ToolsDisabled: true}
	if _, err := m.ensureLive(context.Background(), c1); err != nil {
		t.Fatalf("ensureLive c1: %v", err)
	}
	if _, err := m.ensureLive(context.Background(), c2); err != nil {
		t.Fatalf("ensureLive c2: %v", err)
	}
	m.mu.Lock()
	_, has1 := m.live["c1"]
	_, has2 := m.live["c2"]
	m.mu.Unlock()
	if has1 || !has2 {
		t.Fatalf("cap=1: c1 deveria ter sido evictada, c2 ativa (has1=%v has2=%v)", has1, has2)
	}
}
```
> Para os testes do pool funcionarem com o fake, `ensureLive` precisa propagar os hooks de teste. Faça `ensureLive` setar `ls.testArgs`/`ls.testEnv` quando `m.s.cfg.ClaudeBin == os.Args[0]` (o fake). Detalhe no Step 4.

- [ ] **Step 3: Ler `CLAUDE_MAX_LIVE` no config**

Em `config.go`, adicionar campo `MaxLive int` ao struct `Config` e, na função que monta o config (seguir o padrão dos outros campos no arquivo):
```go
	cfg.MaxLive = envInt("CLAUDE_MAX_LIVE", 4)
```
Se não houver helper `envInt` no arquivo, adicione-o:
```go
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
```
(importar `"os"` e `"strconv"` se ainda não estiverem.)

- [ ] **Step 4: Implementar `livepool.go`**

```go
package main

import (
	"context"
	"os"
	"syscall"
	"time"
)

func (m *SessionManager) maxLive() int {
	if m.s.cfg.MaxLive > 0 {
		return m.s.cfg.MaxLive
	}
	return 4
}

// ensureLive devolve a sessão viva da conversa, criando-a (spawn) se necessário.
// Se o pool está cheio, hiberna a sessão ociosa menos recentemente usada (LRU).
func (m *SessionManager) ensureLive(ctx context.Context, conv *Conversation) (*liveSession, error) {
	m.mu.Lock()
	if ls := m.live[conv.ID]; ls != nil {
		m.mu.Unlock()
		return ls, nil
	}
	if len(m.live) >= m.maxLive() {
		if victim := m.lruIdleLocked(); victim != "" {
			ls := m.live[victim]
			delete(m.live, victim)
			m.mu.Unlock()
			killProcessGroup(ls.cmd) // hiberna; ressuscita via --resume na próxima msg
			m.mu.Lock()
		}
	}
	ls := m.newLiveSession(conv)
	// hooks de teste: quando o ClaudeBin é o próprio binário de teste, fala com o fake.
	if m.s.cfg.ClaudeBin == os.Args[0] {
		ls.testArgs = []string{"-test.run=TestHelperProcess"}
		ls.testEnv = append(os.Environ(), "GO_WANT_FAKE_CLAUDE=1")
	}
	m.live[conv.ID] = ls
	m.mu.Unlock()

	if err := ls.start(ctx); err != nil {
		m.mu.Lock()
		delete(m.live, conv.ID)
		m.mu.Unlock()
		return nil, err
	}
	return ls, nil
}

// lruIdleLocked devolve o convID da sessão idle menos recentemente usada (mu já travado).
// Retorna "" se nenhuma está idle.
func (m *SessionManager) lruIdleLocked() string {
	var oldest string
	var oldestT time.Time
	for id, ls := range m.live {
		ls.mu.Lock()
		idle := ls.state == StatusIdle
		used := ls.lastUsed
		ls.mu.Unlock()
		if !idle {
			continue
		}
		if oldest == "" || used.Before(oldestT) {
			oldest, oldestT = id, used
		}
	}
	return oldest
}

// Evict mata e remove a sessão viva de uma conversa (usado por /clear, /compact, /model
// — que mudam parâmetros de boot, exigindo reinício). A próxima mensagem ressuscita.
func (m *SessionManager) Evict(convID string) {
	m.mu.Lock()
	ls := m.live[convID]
	delete(m.live, convID)
	m.mu.Unlock()
	if ls != nil {
		signalProcessGroup(ls.cmd, syscall.SIGTERM)
	}
}
```

- [ ] **Step 5: Rodar e ver passar**

Run: `cd apps/agent-go && go test ./... -run 'TestEnsureLive' -v`
Expected: PASS (reuso e evict-LRU).

- [ ] **Step 6: Commit**

```bash
cd apps/agent-go && gofmt -w . && go vet ./... && go build ./... && go test ./...
git add session.go config.go livepool.go livesession_test.go
git commit -m "feat(agent-go): pool de sessões vivas com cap e evict LRU"
```

---

## Task 7: Reaper de idle (hibernação) + shutdown gracioso

Goroutine periódica que hiberna sessões ociosas há mais que o TTL e sem WS conectado. Mais um `close` gracioso e o desligamento de todas no shutdown.

**Files:**
- Modify: `apps/agent-go/livepool.go`
- Modify: `apps/agent-go/livesession.go` (método `close`)
- Modify: `apps/agent-go/config.go` (`CLAUDE_IDLE_TTL`)
- Modify: `apps/agent-go/server.go` (iniciar o reaper no boot)
- Test: `apps/agent-go/livesession_test.go`

**Interfaces:**
- Consumes: `m.hasSubs(convID)` (existe em `session.go`), `m.live`, `ls.lastUsed`, `ls.state`.
- Produces:
  - `func (ls *liveSession) close()` — fecha o stdin (saída limpa que salva a sessão).
  - `func (m *SessionManager) reapIdle(ttl time.Duration)` — uma passada de hibernação.
  - `func (m *SessionManager) StartReaper(ctx context.Context)` — loop periódico.
  - `func (m *SessionManager) ShutdownLive()` — fecha todas no shutdown.

- [ ] **Step 1: Escrever o teste do reaper**

Em `livesession_test.go`:
```go
func TestReapIdleHibernaSessaoOciosa(t *testing.T) {
	m := liveTestManager(t)
	m.s.cfg.MaxLive = 4
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: t.TempDir(), ToolsDisabled: true}
	ls, err := m.ensureLive(context.Background(), conv)
	if err != nil {
		t.Fatalf("ensureLive: %v", err)
	}
	// força ociosidade antiga e estado idle
	ls.mu.Lock()
	ls.state = StatusIdle
	ls.lastUsed = time.Now().Add(-time.Hour)
	ls.mu.Unlock()

	m.reapIdle(15 * time.Minute) // TTL menor que 1h => deve hibernar

	m.mu.Lock()
	_, still := m.live["c1"]
	m.mu.Unlock()
	if still {
		t.Fatalf("sessão ociosa > TTL deveria ter sido hibernada")
	}
}

func TestReapIdleNaoHibernaRunning(t *testing.T) {
	m := liveTestManager(t)
	m.s.cfg.MaxLive = 4
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: t.TempDir(), ToolsDisabled: true}
	ls, _ := m.ensureLive(context.Background(), conv)
	ls.mu.Lock()
	ls.state = StatusRunning
	ls.lastUsed = time.Now().Add(-time.Hour)
	ls.mu.Unlock()
	m.reapIdle(15 * time.Minute)
	m.mu.Lock()
	_, still := m.live["c1"]
	m.mu.Unlock()
	if !still {
		t.Fatalf("sessão RUNNING não deveria ser hibernada mesmo ociosa")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/agent-go && go test ./... -run TestReapIdle -v`
Expected: FAIL — `m.reapIdle undefined`.

- [ ] **Step 3: Implementar `close` e o reaper**

Em `livesession.go`:
```go
// close encerra a sessão de forma limpa: fecha o stdin, o que faz o CLI sair e salvar
// a sessão no disco (base da ressurreição via --resume).
func (ls *liveSession) close() {
	if ls.stdin != nil {
		_ = ls.stdin.Close()
	}
}
```
Em `livepool.go` (adicionar imports `"log/slog"`):
```go
// reapIdle hiberna sessões idle ociosas há mais que ttl e sem ninguém conectado (WS).
func (m *SessionManager) reapIdle(ttl time.Duration) {
	now := time.Now()
	var victims []*liveSession
	m.mu.Lock()
	for id, ls := range m.live {
		ls.mu.Lock()
		idle := ls.state == StatusIdle && now.Sub(ls.lastUsed) > ttl
		ls.mu.Unlock()
		if idle && !m.hasSubs(id) {
			victims = append(victims, ls)
			delete(m.live, id)
		}
	}
	m.mu.Unlock()
	for _, ls := range victims {
		slog.Info("hibernando sessão viva ociosa", "conv", ls.conv.ID)
		ls.close()
	}
}

// StartReaper roda reapIdle periodicamente até o ctx ser cancelado.
func (m *SessionManager) StartReaper(ctx context.Context) {
	ttl := 15 * time.Minute
	if m.s.cfg.IdleTTL > 0 {
		ttl = m.s.cfg.IdleTTL
	}
	go func() {
		t := time.NewTicker(ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.reapIdle(ttl)
			}
		}
	}()
}

// ShutdownLive fecha todas as sessões vivas (graceful) no desligamento do servidor.
func (m *SessionManager) ShutdownLive() {
	m.mu.Lock()
	all := make([]*liveSession, 0, len(m.live))
	for id, ls := range m.live {
		all = append(all, ls)
		delete(m.live, id)
	}
	m.mu.Unlock()
	for _, ls := range all {
		ls.close()
	}
}
```

- [ ] **Step 4: Ler `CLAUDE_IDLE_TTL` no config**

Em `config.go`, adicionar campo `IdleTTL time.Duration` ao `Config` e:
```go
	cfg.IdleTTL = envDuration("CLAUDE_IDLE_TTL", 15*time.Minute)
```
Adicionar helper se não existir (importar `"time"`):
```go
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
```

- [ ] **Step 5: Iniciar o reaper no boot**

Em `server.go`, onde o servidor é iniciado (após `s.mgr` existir e antes/junto do `ListenAndServe`), adicionar:
```go
	s.mgr.StartReaper(ctx)
```
Garanta que há um `ctx` de vida do servidor; se o boot usa `context.Background()`, use-o. No caminho de shutdown gracioso (se houver `Shutdown`), chamar `s.mgr.ShutdownLive()`.

- [ ] **Step 6: Rodar e ver passar**

Run: `cd apps/agent-go && go test ./... -run TestReapIdle -v`
Expected: PASS (hiberna idle; preserva running).

- [ ] **Step 7: Commit**

```bash
cd apps/agent-go && gofmt -w . && go vet ./... && go build ./... && go test ./...
git add livesession.go livepool.go config.go server.go livesession_test.go
git commit -m "feat(agent-go): reaper de idle e shutdown gracioso das sessões vivas"
```

---

## Task 8: Ligar ao WebSocket e aos handlers de controle

Troca o caminho de produção do motor antigo (`RunTurn` por turno) pelo motor vivo. WS: `prompt`→`Send`, `interrupt`→`Stop`. Controle: `/clear`,`/compact`,`/model` chamam `Evict`. `/compact` passa a usar a sessão viva para coletar o resumo.

**Files:**
- Modify: `apps/agent-go/ws.go:87-96`
- Modify: `apps/agent-go/handlers_control.go` (`handleSetModel`, `handleClear`, `handleCompact`, `handleStopAll`)
- Modify: `apps/agent-go/session.go` (`RunTurnCollect` usa o motor vivo)
- Test: `apps/agent-go/livesession_test.go`

**Interfaces:**
- Consumes: `m.ensureLive`, `ls.Send`, `ls.Stop`, `m.Evict`.
- Produces: caminho de produção ligado; sem novas funções públicas além das já criadas.

- [ ] **Step 1: WS usa o motor vivo**

Em `ws.go`, substituir o bloco `switch msg.Type` (linhas 86-97):
```go
		switch msg.Type {
		case "prompt":
			fresh, err := s.conversationByID(ctx, id, uid)
			if err != nil || fresh == nil {
				s.mgr.dispatch(conv.ID, turnEvent{Type: "error", Code: "NOT_FOUND", Message: "Conversa não encontrada"})
				continue
			}
			ls, err := s.mgr.ensureLive(ctx, fresh)
			if err != nil {
				s.mgr.dispatch(conv.ID, turnEvent{Type: "error", Code: "SPAWN_FAILED", Message: err.Error()})
				continue
			}
			ls.Send(msg.Text, msg.Attachments)
		case "interrupt":
			s.mgr.mu.Lock()
			ls := s.mgr.live[id]
			s.mgr.mu.Unlock()
			if ls != nil {
				ls.Stop()
			}
		}
```

- [ ] **Step 2: `/clear`, `/compact`, `/model`, stop-all evictam/interrompem a sessão viva**

Em `handlers_control.go`:
- No fim de `handleClear`, **antes** do `writeJSON`, adicionar:
```go
	s.mgr.Evict(conv.ID) // rotacionou session_id: reinicia a sessão viva (ressuscita com --session-id novo)
```
- No fim de `handleSetModel`, antes do `writeJSON`:
```go
	s.mgr.Evict(conv.ID) // modelo é flag de boot: reinicia para o novo modelo valer
```
- Em `handleStopAll`, trocar o corpo por interromper turnos vivos também:
```go
func (s *Server) handleStopAll(w http.ResponseWriter, r *http.Request) {
	s.mgr.mu.Lock()
	live := make([]*liveSession, 0, len(s.mgr.live))
	for _, ls := range s.mgr.live {
		live = append(live, ls)
	}
	s.mgr.mu.Unlock()
	for _, ls := range live {
		ls.Stop()
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": len(live) + s.mgr.InterruptAll()})
}
```

- [ ] **Step 3: `RunTurnCollect` (usado por `/compact`) roda na sessão viva**

Em `session.go`, substituir o corpo de `RunTurnCollect` para enviar pela sessão viva em vez de `go m.RunTurn`:
```go
// RunTurnCollect roda um turno e coleta o texto do assistente (usado pelo /compact),
// agora pela sessão viva da conversa.
func (m *SessionManager) RunTurnCollect(conv *Conversation, prompt string) (string, error) {
	events, unsub := m.Subscribe(conv.ID)
	defer unsub()
	ls, err := m.ensureLive(context.Background(), conv)
	if err != nil {
		return "", err
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
```
Em `handleCompact`, manter a sequência atual (coleta → `rotateSession` → seed); adicionar após o `rotateSession`:
```go
	s.mgr.Evict(conv.ID) // nova sessão semeada: reinicia o processo vivo
```

- [ ] **Step 4: Teste de fumaça do caminho WS→ensureLive→Send**

Em `livesession_test.go`:
```go
func TestEnsureLiveDepoisSendProduzResultado(t *testing.T) {
	m := liveTestManager(t)
	m.s.cfg.MaxLive = 4
	conv := &Conversation{ID: "cWS", SessionID: "s1", Model: "sonnet", Workdir: t.TempDir(), ToolsDisabled: true}
	events, unsub := m.Subscribe(conv.ID)
	defer unsub()
	ls, err := m.ensureLive(context.Background(), conv)
	if err != nil {
		t.Fatalf("ensureLive: %v", err)
	}
	ls.Send("oi", nil)
	if got := collectResults(t, events, 1); len(got) != 1 {
		t.Fatalf("esperava 1 result pelo caminho ensureLive+Send")
	}
}
```

- [ ] **Step 5: Rodar a suíte inteira e ver passar**

Run: `cd apps/agent-go && go test ./... -v`
Expected: PASS — incluindo os testes antigos de `claudeArgs` (intocados) e os novos.

- [ ] **Step 6: Commit**

```bash
cd apps/agent-go && gofmt -w . && go vet ./... && go build ./... && go test ./...
git add ws.go handlers_control.go session.go livesession_test.go
git commit -m "feat(agent-go): WS e controles usam o motor de sessão viva"
```

---

## Task 9: Validação ponta-a-ponta contra o CLI real

Script manual que prova os 4 cenários do spec usando o serviço de verdade. Não é teste automatizado (depende de OAuth/Postgres/Redis) — é um checklist reproduzível.

**Files:**
- Create: `apps/agent-go/docs/validacao-motor-vivo.md`

- [ ] **Step 1: Subir o serviço local**

Run:
```bash
cd apps/agent-go
docker compose -f ../../infra/docker-compose.yml up -d postgres redis
CLAUDE_MAX_LIVE=2 CLAUDE_IDLE_TTL=30s go run .
```
Pré-requisito: usuário role=3 e `claude` logado (volume/credenciais). Anotar a porta.

- [ ] **Step 2: Provar sessão viva (sem cold start no 2º turno)**

Conectar ao WS de uma conversa (via `wscat` ou o `agent-mobile`) e mandar dois prompts seguidos. Confirmar nos logs que o **2º prompt NÃO faz spawn novo** (não aparece novo processo `claude`; só uma nova mensagem no stdin). Anotar a diferença de latência até o primeiro `delta`.

- [ ] **Step 3: Provar fila + botão parar**

Mandar um prompt longo ("conte de 1 a 100 devagar"); durante o turno, mandar `{type:"prompt"}` de novo (deve vir `queued`) e depois `{type:"interrupt"}` (deve parar). Confirmar que após o `interrupt` um novo prompt ainda funciona (processo vivo).

- [ ] **Step 4: Provar hibernação + ressurreição**

Deixar a conversa ociosa > `CLAUDE_IDLE_TTL` (30s). Confirmar no log `hibernando sessão viva ociosa` e que o processo `claude` sumiu (`pgrep -af claude`). Mandar novo prompt e confirmar que ressuscita via `--resume` mantendo o contexto anterior.

- [ ] **Step 5: Registrar resultados e fechar**

Preencher `apps/agent-go/docs/validacao-motor-vivo.md` com o resultado de cada cenário. Atualizar a seção "Modelo de orquestração" do `apps/agent-go/CLAUDE.md` para descrever o motor vivo (substituindo o "por-turno"). Atualizar a seção "Claude Agent" do `../api-go/llms.txt` se algum evento WS novo (`queued`) for documentado.

```bash
cd apps/agent-go && gofmt -l . && go vet ./... && go build ./... && go test ./...
git add docs/validacao-motor-vivo.md CLAUDE.md ../api-go/llms.txt
git commit -m "docs(agent-go): validação e2e do motor de sessão viva + CLAUDE.md"
```

---

## Self-Review (preenchido)

**Spec coverage:**
- Motor vivo (`--input-format stream-json`, processo por conversa) → Tasks 2, 4.
- Hibernação + ressurreição via `--resume` → Tasks 6 (ressuscita), 7 (hiberna).
- Escala/pool/cap/evict LRU → Task 6.
- Msg no meio: fila + botão parar → Tasks 4 (fila), 5 (Stop), 8 (WS).
- Protocolo WS retrocompatível + `queued` opcional → Tasks 4, 8.
- Persistência inalterada → Task 4 (reusa `handleEvent`/`insertMessage`).
- `/clear`,`/compact`,`/model` reiniciam a sessão → Task 8 (`Evict`).
- Riscos #1–#4 (interrupt, formato input, flush ao fechar stdin, fim de turno) → Task 1 (spike) + ajustes nas Tasks 4/5.
- Validação por script (4 cenários) → Task 9.
- Fronteira claude-remote (não compartilhar agora) → decisão de design; `liveSession`/`livepool` já isolados, extraíveis depois. Sem task (intencional).

**Placeholder scan:** sem TBD/TODO; todo step de código tem o código. As únicas decisões deixadas ao executor são explícitas e guiadas (formato exato do input e mecanismo de interrupt), resolvidas pelo spike da Task 1 com instruções de ajuste.

**Type consistency:** `liveSession`, `pendingMsg`, `userMessageJSON`, `newLiveSession`, `start`, `Send`, `Stop`, `close`, `readLoop`, `onTurnEnd`, `ensureLive`, `Evict`, `reapIdle`, `StartReaper`, `ShutdownLive`, `maxLive`, `lruIdleLocked` — nomes e assinaturas consistentes entre as tasks. Campos de `Config`: `MaxLive int`, `IdleTTL time.Duration`. Status reusam `StatusIdle`/`StatusRunning`.
