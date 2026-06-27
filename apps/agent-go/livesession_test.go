package main

import (
	"bufio"
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
