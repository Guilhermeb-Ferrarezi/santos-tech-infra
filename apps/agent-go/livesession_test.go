package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
