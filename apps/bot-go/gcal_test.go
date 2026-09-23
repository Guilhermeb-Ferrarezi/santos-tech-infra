package main

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// access_type=offline + prompt=consent juntos são o que garante REFRESH token.
// Sem os dois, a segunda autorização da mesma conta volta só com access token
// de uma hora — e a integração morre sozinha, sem erro visível.
func TestURLDeAutorizacaoPedeRefreshToken(t *testing.T) {
	g := NewGCalClient("id", "segredo", "https://exemplo/cb", nil)
	u, err := url.Parse(g.URLDeAutorizacao("abc123", UsoAgenda))
	if err != nil {
		t.Fatalf("URL inválida: %v", err)
	}
	q := u.Query()
	if q.Get("access_type") != "offline" {
		t.Error("sem access_type=offline o Google não devolve refresh token")
	}
	if q.Get("prompt") != "consent" {
		t.Error("sem prompt=consent a reautorização não devolve refresh token novo")
	}
	if !strings.Contains(q.Get("scope"), gcalScope) {
		t.Errorf("escopo do calendar ausente: %q", q.Get("scope"))
	}
	if q.Get("state") != "abc123" {
		t.Error("state não foi propagado")
	}
	if q.Get("redirect_uri") != "https://exemplo/cb" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestEnabledExigeAsTresCoisas(t *testing.T) {
	casos := []struct {
		id, seg, red string
		quer         bool
	}{
		{"id", "seg", "url", true},
		{"", "seg", "url", false},
		{"id", "", "url", false},
		{"id", "seg", "", false},
	}
	for _, c := range casos {
		if got := NewGCalClient(c.id, c.seg, c.red, nil).Enabled(); got != c.quer {
			t.Errorf("Enabled(%q,%q,%q) = %v", c.id, c.seg, c.red, got)
		}
	}
	var nilo *GCalClient
	if nilo.Enabled() {
		t.Error("cliente nil deveria estar desabilitado")
	}
}

// O state é de uso único: um link de autorização não pode valer duas vezes.
func TestStateDeUsoUnico(t *testing.T) {
	e := &estadosPendentes{itens: map[string]time.Time{}}
	s := e.novo(UsoAgenda)
	uso, ok := e.consome(s)
	if !ok {
		t.Fatal("o state recém-criado deveria valer")
	}
	if uso != UsoAgenda {
		t.Errorf("o propósito não sobreviveu à viagem: %q", uso)
	}
	if _, ok := e.consome(s); ok {
		t.Error("o mesmo state valeu duas vezes")
	}
	if _, ok := e.consome("inventado"); ok {
		t.Error("state desconhecido não pode passar")
	}
	if _, ok := e.consome(""); ok {
		t.Error("state vazio não pode passar")
	}

	// O propósito viaja no state; o do Drive tem que voltar como Drive, senão
	// a conta da diretoria seria gravada como agenda.
	sd := e.novo(UsoDrive)
	if uso, ok := e.consome(sd); !ok || uso != UsoDrive {
		t.Errorf("propósito do Drive voltou como %q (ok=%v)", uso, ok)
	}
}

func TestStateExpirado(t *testing.T) {
	e := &estadosPendentes{itens: map[string]time.Time{"velho": time.Now().Add(-time.Minute)}}
	if _, ok := e.consome("velho"); ok {
		t.Error("state vencido não pode passar")
	}
}

func TestDescricaoDoEventoLevaOContexto(t *testing.T) {
	d := descricaoDoEvento(&SchedulingRequest{
		Course: "Tecnologia Júnior", Age: 9,
		Notes: "Pai procurando curso para o filho. Gosta de Minecraft.",
	})
	for _, esperado := range []string{"Tecnologia Júnior", "9", "Minecraft", "Notion"} {
		if !strings.Contains(d, esperado) {
			t.Errorf("descrição não menciona %q:\n%s", esperado, d)
		}
	}
	// Sem dados, não inventa seções vazias.
	if d := descricaoDoEvento(&SchedulingRequest{}); strings.Contains(d, "Idade") {
		t.Errorf("descrição inventou idade que não existe:\n%s", d)
	}
}
