package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// askSession sobe uma sessão viva com o fake em modo FAKE_ASK (o turno chama
// AskUserQuestion e espera o host) e já manda o 1º prompt.
func askSession(t *testing.T) (*SessionManager, *liveSession, <-chan turnEvent) {
	t.Helper()
	m := liveTestManager(t)
	conv := &Conversation{ID: "cq", SessionID: "s1", Model: "sonnet", Workdir: t.TempDir(), Kind: designKind}
	ls := m.newLiveSession(conv)
	ls.testArgs = []string{"-test.run=TestHelperProcess"}
	ls.testEnv = fakeEnv("FAKE_ASK=1")
	events, unsub := m.Subscribe(conv.ID)
	t.Cleanup(unsub)
	if err := ls.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	m.mu.Lock()
	m.live[conv.ID] = ls
	m.mu.Unlock()
	t.Cleanup(ls.close)
	ls.Send("desenha um app", nil)
	return m, ls, events
}

// waitEvent lê até achar um evento do tipo dado e o devolve.
func waitEvent(t *testing.T, events <-chan turnEvent, typ string) turnEvent {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == typ {
				return ev
			}
		case <-timeout:
			t.Fatalf("não chegou evento %q", typ)
		}
	}
}

// toolResultEco decodifica o eco que o fake põe no tool_result.
func toolResultEco(t *testing.T, ev turnEvent) map[string]any {
	t.Helper()
	block, _ := ev.Data.(map[string]any)
	raw, _ := block["content"].(string)
	var eco map[string]any
	if err := json.Unmarshal([]byte(raw), &eco); err != nil {
		t.Fatalf("tool_result sem eco: %v (%q)", err, raw)
	}
	return eco
}

func TestPerguntaPausaTurnoEDesarmaWatchdog(t *testing.T) {
	_, ls, events := askSession(t)
	q := waitEvent(t, events, "question")
	data := q.Data.(map[string]any)
	if data["requestId"] != "req-1" || data["toolUseId"] != "toolu_ask" {
		t.Fatalf("evento de pergunta = %+v", data)
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.state != StatusRunning {
		t.Fatalf("turno deveria seguir running enquanto espera a resposta; state=%s", ls.state)
	}
	if ls.watchdog != nil {
		t.Fatal("watchdog deveria estar desarmado enquanto a pergunta espera o humano")
	}
}

func TestPerguntaRespondidaContinuaOTurnoComAnswers(t *testing.T) {
	_, ls, events := askSession(t)
	waitEvent(t, events, "question")

	ok := ls.Answer("req-1", map[string]string{"Qual plataforma?": " iOS ", "pergunta que não existe": "x"}, false)
	if !ok {
		t.Fatal("Answer devolveu false para a pergunta pendente")
	}
	waitEvent(t, events, "question_closed")
	eco := toolResultEco(t, waitEvent(t, events, "tool_result"))
	if eco["behavior"] != "allow" || eco["request_id"] != "req-1" {
		t.Fatalf("resposta ao CLI = %+v", eco)
	}
	answers, _ := eco["answers"].(map[string]any)
	if len(answers) != 1 || answers["Qual plataforma?"] != "iOS" {
		t.Fatalf("answers = %+v (esperava só a pergunta válida, aparada)", answers)
	}
	waitEvent(t, events, "result")
	if ls.pendingQuestionEvent() != nil {
		t.Fatal("pendência deveria ter sido limpa")
	}
}

func TestPerguntaPuladaRecusaPedindoQueOModeloDecida(t *testing.T) {
	_, ls, events := askSession(t)
	waitEvent(t, events, "question")
	if !ls.Answer("req-1", nil, true) {
		t.Fatal("Answer(skip) devolveu false")
	}
	eco := toolResultEco(t, waitEvent(t, events, "tool_result"))
	if eco["behavior"] != "deny" || eco["message"] != msgPerguntaPulada {
		t.Fatalf("pular deveria recusar com a mensagem de decidir sozinho; veio %+v", eco)
	}
}

func TestPerguntaSemRespostaValidaViraPulada(t *testing.T) {
	_, ls, events := askSession(t)
	waitEvent(t, events, "question")
	ls.Answer("req-1", map[string]string{"Qual plataforma?": "   "}, false)
	if eco := toolResultEco(t, waitEvent(t, events, "tool_result")); eco["behavior"] != "deny" {
		t.Fatalf("resposta vazia deveria virar recusa; veio %+v", eco)
	}
}

func TestPerguntaRequestIDErradoNaoResponde(t *testing.T) {
	_, ls, events := askSession(t)
	waitEvent(t, events, "question")
	if ls.Answer("req-99", map[string]string{"Qual plataforma?": "iOS"}, false) {
		t.Fatal("Answer com requestId errado deveria devolver false")
	}
	if ls.pendingQuestionEvent() == nil {
		t.Fatal("a pergunta verdadeira não pode ter sido consumida")
	}
}

func TestPerguntaPendenteEReenviadaPeloManager(t *testing.T) {
	m, _, events := askSession(t)
	waitEvent(t, events, "question")
	ev := m.PendingQuestion("cq")
	if ev == nil || ev.Type != "question" {
		t.Fatalf("PendingQuestion = %+v", ev)
	}
	if m.PendingQuestion("outra-conversa") != nil {
		t.Fatal("conversa sem sessão não tem pergunta")
	}
}

func TestStopComPerguntaPendenteRecusaAntesDeInterromper(t *testing.T) {
	_, ls, events := askSession(t)
	waitEvent(t, events, "question")
	ls.Stop()
	eco := toolResultEco(t, waitEvent(t, events, "tool_result"))
	if eco["behavior"] != "deny" || eco["message"] != msgPerguntaParada {
		t.Fatalf("Stop deveria recusar a pergunta antes do interrupt; veio %+v", eco)
	}
}

// bufCloser é um stdin falso que registra o que o host escreveu.
type bufCloser struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *bufCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
func (w *bufCloser) Close() error { return nil }
func (w *bufCloser) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func TestOutraFerramentaInterativaERecusadaNaHora(t *testing.T) {
	m := liveTestManager(t)
	ls := m.newLiveSession(&Conversation{ID: "cx"})
	stdin := &bufCloser{}
	ls.stdin = stdin
	ls.handleControlRequest(map[string]any{
		"type": "control_request", "request_id": "r1",
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "ExitPlanMode", "input": map[string]any{}},
	})
	out := stdin.String()
	if !strings.Contains(out, `"behavior":"deny"`) || !strings.Contains(out, `"request_id":"r1"`) {
		t.Fatalf("ferramenta desconhecida deveria ser recusada; escrito: %s", out)
	}
	if ls.pendingQuestionEvent() != nil {
		t.Fatal("não pode virar pergunta pendente")
	}
}

func TestControlRequestDesconhecidoRespondeErro(t *testing.T) {
	m := liveTestManager(t)
	ls := m.newLiveSession(&Conversation{ID: "cx"})
	stdin := &bufCloser{}
	ls.stdin = stdin
	ls.handleControlRequest(map[string]any{
		"type": "control_request", "request_id": "r2",
		"request": map[string]any{"subtype": "hook_callback"},
	})
	if out := stdin.String(); !strings.Contains(out, `"subtype":"error"`) {
		t.Fatalf("subtipo desconhecido deveria responder erro; escrito: %s", out)
	}
}

func TestClaudeArgsLiveDesignLigaPerguntasEPrompt(t *testing.T) {
	m := liveTestManager(t)
	design := m.claudeArgsLive(&Conversation{ID: "d", SessionID: "s", Model: "sonnet", Workdir: t.TempDir(), Kind: designKind}, "")
	i := slices.Index(design, "--permission-prompt-tool")
	if i < 0 || design[i+1] != "stdio" {
		t.Fatalf("design sem --permission-prompt-tool stdio: %v", design)
	}
	j := slices.Index(design, "--append-system-prompt")
	if j < 0 || !strings.Contains(design[j+1], "AskUserQuestion") {
		t.Fatal("design sem o prompt de sistema do Claude Design")
	}

	chat := m.claudeArgsLive(&Conversation{ID: "c", SessionID: "s", Model: "sonnet", Workdir: t.TempDir(), Kind: "chat"}, "")
	if slices.Contains(chat, "--permission-prompt-tool") || slices.Contains(chat, "--append-system-prompt") {
		t.Fatalf("chat comum não deve mudar: %v", chat)
	}
}

func TestSanitizeAnswers(t *testing.T) {
	input := map[string]any{"questions": []any{
		map[string]any{"question": "A?"}, map[string]any{"question": "B?"},
	}}
	longa := strings.Repeat("é", maxAnswerRunes+10)
	got := sanitizeAnswers(input, map[string]string{"A?": "  sim ", "B?": "", "C?": "invasão", "": "x"})
	if len(got) != 1 || got["A?"] != "sim" {
		t.Fatalf("sanitize = %+v", got)
	}
	if r := []rune(sanitizeAnswers(input, map[string]string{"B?": longa})["B?"]); len(r) != maxAnswerRunes {
		t.Fatalf("resposta longa deveria ser cortada em %d runes, veio %d", maxAnswerRunes, len(r))
	}
}
