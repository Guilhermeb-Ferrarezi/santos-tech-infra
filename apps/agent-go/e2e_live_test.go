//go:build e2e_live

// E2E do motor de sessão viva contra o CLI `claude` REAL (sem Postgres/Redis — a
// persistência é nil-guarded). Cobre os 4 cenários da Task 9 no nível do motor:
// sessão viva sem cold start, fila, parar sem matar o processo, e hibernação→ressurreição
// via --resume. Opt-in (não roda na suíte normal):
//
//	export PATH="$HOME/.local/bin:$PATH"
//	cd apps/agent-go && go test -tags e2e_live -run TestE2ELive -v -timeout 5m
//
// Requer o `claude` logado no ambiente (usa ~/.claude via HOME no claudeEnv).
package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func e2eManager(t *testing.T) (*SessionManager, *Conversation) {
	t.Helper()
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	s := &Server{cfg: Config{
		ClaudeBin: bin, DefaultModel: "sonnet",
		WorkspaceRoot: t.TempDir(), MaxLive: 4,
	}}
	m := newSessionManager(s)
	wd := t.TempDir()
	conv := &Conversation{
		ID: "e2e-" + newUUID(), SessionID: newUUID(),
		Model: "sonnet", Workdir: wd, ToolsDisabled: false,
	}
	return m, conv
}

// collectUntilDone drena eventos até "done" (ou timeout), devolvendo o texto agregado
// dos deltas/result e os tipos vistos.
func collectUntilDone(t *testing.T, events <-chan turnEvent, d time.Duration) (string, []string) {
	t.Helper()
	var b strings.Builder
	var types []string
	timeout := time.After(d)
	for {
		select {
		case ev := <-events:
			types = append(types, ev.Type)
			if ev.Type == "delta" {
				b.WriteString(ev.Text)
			}
			if ev.Type == "done" {
				return b.String(), types
			}
			if ev.Type == "error" {
				t.Fatalf("evento de erro no turno: %s", ev.Message)
			}
		case <-timeout:
			t.Fatalf("timeout esperando 'done'; tipos vistos: %v", types)
		}
	}
}

func TestE2ELiveSemColdStartEFila(t *testing.T) {
	m, conv := e2eManager(t)
	events, unsub := m.Subscribe(conv.ID)
	defer unsub()
	ls, err := m.ensureLive(context.Background(), conv)
	if err != nil {
		t.Fatalf("ensureLive: %v", err)
	}
	defer ls.close()

	// Turno 1
	ls.Send("responda apenas: UM", nil)
	txt1, _ := collectUntilDone(t, events, 180*time.Second)
	t.Logf("turno 1: %q", strings.TrimSpace(txt1))

	// Mesmo processo segue vivo no pool (sem cold start no turno 2)
	m.mu.Lock()
	same := m.live[conv.ID] == ls
	m.mu.Unlock()
	if !same {
		t.Fatalf("esperava o MESMO processo vivo no pool após turno 1 (sem cold start)")
	}

	// Turnos 2 e 3 no MESMO processo vivo (sem cold start). Sequenciais — a fila
	// concorrente é coberta deterministicamente pelo teste unitário (FAKE_DELAY_MS);
	// contra o CLI real o timing não é controlável.
	ls.Send("responda apenas: DOIS", nil)
	txt2, _ := collectUntilDone(t, events, 180*time.Second)
	if !strings.Contains(strings.ToUpper(txt2), "DOIS") {
		t.Fatalf("turno 2 deveria responder DOIS; veio %q", txt2)
	}
	m.mu.Lock()
	same2 := m.live[conv.ID] == ls
	m.mu.Unlock()
	if !same2 {
		t.Fatalf("turno 2 deveria reusar o MESMO processo vivo (sem cold start)")
	}
	ls.Send("responda apenas: TRES", nil)
	txt3, _ := collectUntilDone(t, events, 180*time.Second)
	if !strings.Contains(strings.ToUpper(txt3), "TR") { // "TRES"/"TRÊS" (acento)
		t.Fatalf("turno 3 deveria responder TRES; veio %q", txt3)
	}
	t.Log("multi-turno: 3 turnos no mesmo processo vivo, sem respawn")
}

func TestE2ELiveStopMantemProcessoVivo(t *testing.T) {
	m, conv := e2eManager(t)
	events, unsub := m.Subscribe(conv.ID)
	defer unsub()
	ls, err := m.ensureLive(context.Background(), conv)
	if err != nil {
		t.Fatalf("ensureLive: %v", err)
	}
	defer ls.close()

	ls.Send("conte de 1 ate 200, um numero por linha, devagar", nil)
	time.Sleep(2 * time.Second)
	ls.Stop()
	// turno interrompido encerra com um result → done
	_, _ = collectUntilDone(t, events, 60*time.Second)

	select {
	case <-ls.done:
		t.Fatalf("processo morreu após Stop — não deveria")
	default:
	}

	// processo segue vivo: novo turno funciona
	ls.Send("responda apenas: VIVO", nil)
	txt, _ := collectUntilDone(t, events, 180*time.Second)
	if !strings.Contains(strings.ToUpper(txt), "VIVO") {
		t.Fatalf("esperava resposta do novo turno após Stop; veio %q", txt)
	}
	t.Log("Stop interrompeu o turno e o processo continuou vivo")
}

func TestE2ELiveHibernacaoRessurreicao(t *testing.T) {
	m, conv := e2eManager(t)
	events, unsub := m.Subscribe(conv.ID)
	ls, err := m.ensureLive(context.Background(), conv)
	if err != nil {
		t.Fatalf("ensureLive: %v", err)
	}

	// Turno 1: grava uma palavra-chave na memória da sessão.
	ls.Send("lembre desta palavra-chave: ROXO-42. responda apenas: OK", nil)
	_, _ = collectUntilDone(t, events, 180*time.Second)
	if !conv.SessionStarted {
		t.Fatalf("SessionStarted deveria estar true após o 1º turno (habilita --resume)")
	}

	// Hibernação: fecha o processo e remove do pool (como o reaper faz).
	ls.close()
	<-ls.done
	m.removeLive(conv.ID, ls)
	unsub()
	m.mu.Lock()
	_, still := m.live[conv.ID]
	m.mu.Unlock()
	if still {
		t.Fatalf("conversa deveria ter saído do pool após hibernar")
	}

	// Ressurreição: novo processo via --resume; deve lembrar a palavra-chave.
	events2, unsub2 := m.Subscribe(conv.ID)
	defer unsub2()
	ls2, err := m.ensureLive(context.Background(), conv) // SessionStarted=true → --resume
	if err != nil {
		t.Fatalf("ensureLive (ressurreição): %v", err)
	}
	defer ls2.close()
	if ls2 == ls {
		t.Fatalf("esperava um NOVO processo na ressurreição")
	}
	ls2.Send("qual era a palavra-chave que pedi pra lembrar? responda só a palavra-chave", nil)
	txt, _ := collectUntilDone(t, events2, 90*time.Second)
	if !strings.Contains(strings.ToUpper(txt), "ROXO-42") {
		t.Fatalf("ressurreição via --resume perdeu a memória; veio %q", txt)
	}
	t.Log("hibernação→ressurreição via --resume preservou a memória da sessão")
}
