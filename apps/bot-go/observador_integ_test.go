package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Modo observador contra Postgres real e um modelo simulado.
func TestObservadorIntegracao(t *testing.T) {
	pool, ctx := poolDeTeste(t)
	tenant := novoTenantDeTeste(t, ctx, pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// O "modelo": responde conforme a mensagem, e conta as consultas.
	var mu sync.Mutex
	consultas := 0
	modelo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agentGoRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		consultas++
		mu.Unlock()
		// Só a mensagem do cliente: o prompt tem exemplos com "dezembro".
		msg := req.Brief
		if _, depois, ok := strings.Cut(msg, `Mensagem do cliente: "`); ok {
			msg, _, _ = strings.Cut(depois, "\"\n")
		}
		resp := `{"retorno":null,"compromisso":null}`
		switch {
		case strings.Contains(msg, "dezembro"):
			resp = `{"retorno":{"data":"2026-12-01","confianca":0.5},"compromisso":null}`
		case strings.Contains(msg, "marido"):
			resp = `{"retorno":null,"compromisso":"vai falar com o marido"}`
		}
		b, _ := json.Marshal(map[string]string{"text": "```json\n" + resp + "\n```"})
		_, _ = w.Write(b)
	}))
	defer modelo.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	obs := NewObservador(NewObservadorRepo(pool), NewAgentGoClient(modelo.URL, "x", "", nil, nil), logger)
	obs.agora = func() time.Time { return time.Date(2026, 9, 25, 15, 0, 0, 0, brLocation) }

	// Cliente do número não-oficial, conversa com humano (bot desligado).
	// Telefone único por rodada: o banco de teste roda como superusuário (sem
	// RLS), e o mesmo número de uma rodada anterior seria achado em outro tenant.
	fone := strconv.FormatInt(5511900000000+time.Now().UnixNano()%100000000, 10)
	var contact, ident string
	must(pool.QueryRow(ctx, `INSERT INTO contact (tenant_id, display_name) VALUES ($1, 'Vivian') RETURNING id`, tenant).Scan(&contact))
	must(pool.QueryRow(ctx, `INSERT INTO channel_identity (tenant_id, contact_id, channel, external_id) VALUES ($1, $2, 'evolution', $3) RETURNING id`, tenant, contact, fone).Scan(&ident))
	s := &Server{cfg: Config{TenantID: tenant}, pool: pool, logger: logger, observador: obs,
		contacts: &ContactRepo{pool: pool}, withTenant: withTenant(pool, TenantID(tenant))}

	// Desligado na tela: nada acontece, nem consulta.
	s.observaEvolution(ctx, fone, "me chama em dezembro que consigo mais")
	if consultas != 0 {
		t.Fatalf("com o observador desligado houve %d consultas", consultas)
	}

	_, err := pool.Exec(ctx, `UPDATE tenant_config SET observador_ligado = true WHERE tenant_id = $1`, tenant)
	must(err)

	// "ok" não gasta consulta.
	s.observaEvolution(ctx, fone, "ok")
	if consultas != 0 {
		t.Fatalf("mensagem curta gastou consulta (%d)", consultas)
	}

	// Pedido de retorno: vira retorno às 9h do dia, com conversa criada para o link.
	s.observaEvolution(ctx, fone, "me chama em dezembro que consigo mais")
	var fire time.Time
	var payload string
	var conv *string
	must(pool.QueryRow(ctx, `SELECT fire_at, payload::text, conversation_id::text FROM scheduled_contacts WHERE tenant_id = $1 AND contact_id = $2`, tenant, contact).Scan(&fire, &payload, &conv))
	if got := fire.In(brLocation).Format("2006-01-02 15:04"); got != "2026-12-01 09:00" {
		t.Errorf("retorno para %s, queria 2026-12-01 09:00", got)
	}
	if !strings.Contains(payload, `"origem": "observador"`) || !strings.Contains(payload, "consigo mais") || conv == nil {
		t.Errorf("payload/conversa: %s conv=%v", payload, conv)
	}
	var botLigado bool
	must(pool.QueryRow(ctx, `SELECT bot_enabled FROM conversation WHERE id = $1`, *conv).Scan(&botLigado))
	if botLigado {
		t.Error("a conversa criada pelo observador não pode ligar o bot")
	}

	// Repetido (ou dito de novo dias depois): não duplica.
	s.observaEvolution(ctx, fone, "lembra, em dezembro eu consigo")
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_contacts WHERE tenant_id = $1 AND contact_id = $2`, tenant, contact).Scan(&n))
	if n != 1 {
		t.Errorf("retornos: %d, queria 1", n)
	}

	// Compromisso: vai para a ficha, sem repetir, e sobrevive ao Save do bot.
	s.observaEvolution(ctx, fone, "vou falar com meu marido e te aviso")
	s.observaEvolution(ctx, fone, "então, vou falar com meu marido hoje")
	qrepo := &QualificacaoRepo{pool: pool}
	q, ok := qrepo.Get(ctx, nil, TenantID(tenant), ContactID(contact))
	if !ok || q.Compromissos != "Compromisso (25/09): vai falar com o marido" {
		t.Fatalf("compromissos = %q", q.Compromissos)
	}
	must(qrepo.Save(ctx, TenantID(tenant), ContactID(contact), Qualificacao{Interesse: "Excel", Observacoes: "mora longe"}))
	if q, _ = qrepo.Get(ctx, nil, TenantID(tenant), ContactID(contact)); q.Compromissos == "" || q.Interesse != "Excel" {
		t.Errorf("o Save do bot apagou o compromisso: %+v", q)
	}

	// O card do CRM mostra o compromisso.
	var lead string
	must(pool.QueryRow(ctx, `INSERT INTO lead (tenant_id, contact_id, status) VALUES ($1, $2, 'em_atendimento') RETURNING id`, tenant, contact).Scan(&lead))
	rr := httptest.NewRecorder()
	s.handleDashLeads(rr, httptest.NewRequest(http.MethodGet, "/api/leads", nil))
	var ls []dashLead
	must(json.Unmarshal(rr.Body.Bytes(), &ls))
	achou := false
	for _, l := range ls {
		if l.ID == lead && strings.Contains(l.Compromissos, "vai falar com o marido") {
			achou = true
		}
	}
	if !achou {
		t.Errorf("/api/leads sem o compromisso: %s", rr.Body)
	}

	// Liga/desliga pela tela.
	rr = httptest.NewRecorder()
	s.handleDashPatchConfig(rr, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"observadorLigado":false}`)))
	if rr.Code != 200 {
		t.Fatalf("PATCH observadorLigado = %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleDashGetConfig(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var c dashConfig
	must(json.Unmarshal(rr.Body.Bytes(), &c))
	if c.ObservadorLigado {
		t.Error("observador continuou ligado depois do PATCH")
	}
}
