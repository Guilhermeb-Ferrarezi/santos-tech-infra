package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integração contra Postgres real — mesma regra de reativacao_integ_test.go:
// roda só com BOT_TEST_DATABASE_URL apontando para um banco com as migrations.

func poolDeTeste(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("BOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("BOT_TEST_DATABASE_URL vazio")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func novoTenantDeTeste(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var tenant string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (name, slug) VALUES ('t', 't-'||gen_random_uuid()) RETURNING id`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tenant_config (tenant_id) VALUES ($1)`, tenant); err != nil {
		t.Fatal(err)
	}
	return tenant
}

// O painel salva os três campos; o que não veio fica; 0 no responsável volta
// ao padrão (NULL); valor inválido é 400 e não toca no banco.
func TestConfigFollowupIntegracao(t *testing.T) {
	pool, ctx := poolDeTeste(t)
	tenant := novoTenantDeTeste(t, ctx, pool)
	s := &Server{cfg: Config{TenantID: tenant, FollowUpResponsavelID: 30}, pool: pool, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	patch := func(body string) int {
		rr := httptest.NewRecorder()
		s.handleDashPatchConfig(rr, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(body)))
		return rr.Code
	}
	get := func() dashConfig {
		rr := httptest.NewRecorder()
		s.handleDashGetConfig(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if rr.Code != 200 {
			t.Fatalf("GET /api/config = %d: %s", rr.Code, rr.Body)
		}
		var c dashConfig
		if err := json.Unmarshal(rr.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}

	c := get()
	if c.FollowupModo != "avisar" || c.FollowupDiasPosExperimental != 2 || c.FollowupResponsavelID != 0 || c.FollowupResponsavelPadrao != 30 {
		t.Errorf("padrões errados: %+v", c)
	}

	if code := patch(`{"followupModo":"reativar_e_avisar","followupDiasPosExperimental":5,"followupResponsavelId":12}`); code != 200 {
		t.Fatalf("PATCH válido = %d", code)
	}
	c = get()
	if c.FollowupModo != "reativar_e_avisar" || c.FollowupDiasPosExperimental != 5 || c.FollowupResponsavelID != 12 {
		t.Errorf("não gravou: %+v", c)
	}

	// Painel antigo (sem os campos) não apaga nada.
	if code := patch(`{"botName":"Marcos"}`); code != 200 {
		t.Fatalf("PATCH sem follow-up = %d", code)
	}
	if c = get(); c.FollowupModo != "reativar_e_avisar" || c.FollowupDiasPosExperimental != 5 || c.FollowupResponsavelID != 12 {
		t.Errorf("PATCH sem os campos mexeu neles: %+v", c)
	}

	// 0 = volta ao padrão do ambiente, gravado como NULL.
	if code := patch(`{"followupResponsavelId":0,"followupDiasPosExperimental":0}`); code != 200 {
		t.Fatalf("PATCH zero = %d", code)
	}
	var resp *int
	if err := pool.QueryRow(ctx, `SELECT followup_responsavel_id FROM tenant_config WHERE tenant_id = $1`, tenant).Scan(&resp); err != nil {
		t.Fatal(err)
	}
	if resp != nil {
		t.Errorf("responsável 0 deveria gravar NULL, gravou %d", *resp)
	}
	if c = get(); c.FollowupDiasPosExperimental != 0 {
		t.Errorf("dias 0 (desligado) não gravou: %+v", c)
	}

	for _, ruim := range []string{`{"followupModo":"mandar_tudo"}`, `{"followupDiasPosExperimental":31}`, `{"followupResponsavelId":-1}`} {
		if code := patch(ruim); code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, queria 400", ruim, code)
		}
	}
	if c = get(); c.FollowupModo != "reativar_e_avisar" {
		t.Errorf("PATCH inválido mexeu no banco: %+v", c)
	}

	// O worker lê a mesma configuração.
	cfg, err := (&TenantConfigRepo{pool: pool}).Get(ctx, nil, TenantID(tenant))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FollowupModo != "reativar_e_avisar" || cfg.FollowupResponsavelID != 0 {
		t.Errorf("TenantConfigRepo.Get: %+v", cfg)
	}
}

// Quem entra na fila pós-experimental, e quem não entra.
func TestPosExperimentalIntegracao(t *testing.T) {
	pool, ctx := poolDeTeste(t)
	tenant := novoTenantDeTeste(t, ctx, pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	type pessoa struct{ contact, conv string }
	cria := func(nome, fone string) pessoa {
		var p pessoa
		var ident string
		must(pool.QueryRow(ctx, `INSERT INTO contact (tenant_id, display_name) VALUES ($1, $2) RETURNING id`, tenant, nome).Scan(&p.contact))
		must(pool.QueryRow(ctx, `INSERT INTO channel_identity (tenant_id, contact_id, channel, external_id) VALUES ($1, $2, 'evolution', $3) RETURNING id`, tenant, p.contact, fone).Scan(&ident))
		must(pool.QueryRow(ctx, `INSERT INTO conversation (tenant_id, channel_identity_id, channel, bot_enabled) VALUES ($1, $2, 'evolution', true) RETURNING id`, tenant, ident).Scan(&p.conv))
		return p
	}
	aula := func(pagina, fone string, conv *string, quando time.Time) {
		// Três lembretes por aula, como na vida real — tem que virar UM retorno.
		for _, kind := range []string{"vespera", "quatro_horas", "uma_hora"} {
			_, err := pool.Exec(ctx, `INSERT INTO booking_reminder (tenant_id, notion_page_id, conversation_id, client_phone, channel, aluno, kind, aula_em, enviar_em, status)
				VALUES ($1, $2, $3, $4, 'evolution', 'Aluno '||$2, $5, $6, $6, 'enviado')`, tenant, pagina, conv, fone, kind, quando)
			must(err)
		}
	}
	resultado := func(pagina, r string) {
		_, err := pool.Exec(ctx, `INSERT INTO aula_resultado (tenant_id, notion_page_id, resultado) VALUES ($1, $2, $3)`, tenant, pagina, r)
		must(err)
	}

	// Aula há 3 dias → prazo (N=2) venceu ontem às 9h: dentro da janela.
	recente := time.Now().AddDate(0, 0, -3)
	semResultado := cria("Sem Resultado", "5500000000001")
	aula("p-sem", "5500000000001", &semResultado.conv, recente)
	veio := cria("Veio", "5500000000002")
	aula("p-veio", "5500000000002", nil, recente) // sem conversa na aula: acha pelo telefone
	resultado("p-veio", "veio")
	fechou := cria("Fechou", "5500000000003")
	aula("p-fechou", "5500000000003", &fechou.conv, recente)
	resultado("p-fechou", "fechou")
	faltou := cria("Faltou", "5500000000004")
	aula("p-faltou", "5500000000004", &faltou.conv, recente)
	resultado("p-faltou", "faltou")
	naoFechou := cria("Nao Fechou", "5500000000005")
	aula("p-naofechou", "5500000000005", &naoFechou.conv, recente)
	resultado("p-naofechou", "nao_fechou")
	// Aula há 12 dias: prazo venceu há 10, fora da janela — não vira rajada.
	antiga := cria("Antiga", "5500000000006")
	aula("p-antiga", "5500000000006", &antiga.conv, time.Now().AddDate(0, 0, -12))
	// Aula de hoje: prazo ainda não chegou.
	hoje := cria("Hoje", "5500000000007")
	aula("p-hoje", "5500000000007", &hoje.conv, time.Now().Add(-2*time.Hour))
	// Pediu "me chama em dezembro" depois da aula: o pedido dela vence.
	pediu := cria("Pediu Dezembro", "5500000000008")
	aula("p-pediu", "5500000000008", &pediu.conv, recente)
	_, err := pool.Exec(ctx, `INSERT INTO scheduled_contacts (tenant_id, contact_id, conversation_id, fire_at, payload)
		VALUES ($1, $2, $3, now() + interval '60 days', '{"kind":"reactivation"}')`, tenant, pediu.contact, pediu.conv)
	must(err)

	sched := &ScheduledContactRepo{pool: pool}
	n, err := sched.EnfileiraPosExperimental(ctx, TenantID(tenant), 2)
	must(err)
	if n != 2 {
		t.Errorf("enfileirou %d, queria 2 (sem resultado + veio)", n)
	}
	var paginas []string
	rows, err := pool.Query(ctx, `SELECT payload->>'notionPageId' FROM scheduled_contacts
		WHERE tenant_id = $1 AND payload->>'kind' = 'pos_experimental' ORDER BY 1`, tenant)
	must(err)
	for rows.Next() {
		var p string
		must(rows.Scan(&p))
		paginas = append(paginas, p)
	}
	rows.Close()
	if strings.Join(paginas, ",") != "p-sem,p-veio" {
		t.Errorf("na fila: %v, queria [p-sem p-veio]", paginas)
	}

	// Rodar de novo (outro ciclo, outra réplica) não duplica.
	n, err = sched.EnfileiraPosExperimental(ctx, TenantID(tenant), 2)
	must(err)
	if n != 0 {
		t.Errorf("segundo ciclo enfileirou %d de novo", n)
	}

	// Sai na fila de disparo com o que a decisão precisa.
	ps, err := sched.PendingReactivations(ctx, 100)
	must(err)
	var achou *retornoPendente
	for i := range ps {
		if ps[i].TenantID == TenantID(tenant) && ps[i].Kind == KindPosExperimental && string(ps[i].ConvID) == veio.conv {
			achou = &ps[i]
		}
	}
	if achou == nil {
		t.Fatal("o pós-experimental não apareceu na fila de disparo")
	}
	if achou.AulaEm == nil || achou.Telefone != "5500000000002" || achou.Canal != "evolution" || !achou.BotLigado || achou.Nome != "Veio" {
		t.Errorf("campos errados: %+v", *achou)
	}

	// Depois de disparado, a mesma aula não entra de novo.
	must(sched.MarkFollowUpSent(ctx, achou.ID))
	n, err = sched.EnfileiraPosExperimental(ctx, TenantID(tenant), 2)
	must(err)
	if n != 0 {
		t.Errorf("aula já disparada voltou para a fila (%d)", n)
	}
}

// senderGravador — registra o que sairia pelo WhatsApp.
type senderGravador struct {
	msgs   []OutboundMessage
	textos []string
}

func (s *senderGravador) SendMessage(_ context.Context, m OutboundMessage) (string, error) {
	s.msgs = append(s.msgs, m)
	return "wamid-" + m.IdempotencyKey, nil
}
func (s *senderGravador) SendText(_ context.Context, to, text string) error {
	s.textos = append(s.textos, to+"|"+text)
	return nil
}
func (s *senderGravador) SendTypingIndicator(context.Context, string) error { return nil }

// O caminho inteiro no modo "reativar e avisar": o modelo escreve, a mensagem
// sai pelo canal do cliente, entra no histórico e o humano é avisado. A conversa
// em handoff cai para só avisar, dizendo por quê.
func TestReativarEAvisarIntegracao(t *testing.T) {
	pool, ctx := poolDeTeste(t)
	tenant := novoTenantDeTeste(t, ctx, pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, `UPDATE tenant_config SET followup_modo = 'reativar_e_avisar', evolution_bot_reply_enabled = true,
		admin_whatsapp_numbers = '["5516000000000"]', bot_name = 'Marcos', followup_dias_pos_experimental = 0 WHERE tenant_id = $1`, tenant)
	must(err)

	cria := func(nome, fone, estado string) (contact, conv string) {
		var ident string
		must(pool.QueryRow(ctx, `INSERT INTO contact (tenant_id, display_name) VALUES ($1, $2) RETURNING id`, tenant, nome).Scan(&contact))
		must(pool.QueryRow(ctx, `INSERT INTO channel_identity (tenant_id, contact_id, channel, external_id) VALUES ($1, $2, 'evolution', $3) RETURNING id`, tenant, contact, fone).Scan(&ident))
		must(pool.QueryRow(ctx, `INSERT INTO conversation (tenant_id, channel_identity_id, channel, bot_enabled, state, last_inbound_at)
			VALUES ($1, $2, 'evolution', true, $3::conversation_state, now() - interval '20 days') RETURNING id`, tenant, ident, estado).Scan(&conv))
		_, err := pool.Exec(ctx, `INSERT INTO scheduled_contacts (tenant_id, contact_id, conversation_id, fire_at, payload, created_at)
			VALUES ($1, $2, $3, now() - interval '1 hour', '{"kind":"reactivation","rawPhrase":"me chama no fim do mês","confidence":0.9}', now() - interval '20 days')`,
			tenant, contact, conv)
		must(err)
		return contact, conv
	}
	_, convLivre := cria("Vivian", "5511900000001", "ENGAGED")
	_, convHandoff := cria("Bruno", "5511900000002", "HANDOFF")

	modelo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"\"Oi Vivian! Combinamos de falar agora no fim do mês. Tudo bem por aí?\""}`))
	}))
	defer modelo.Close()

	evo := &senderGravador{}
	w := NewWorker(WorkerDeps{
		Config:          Config{TenantID: tenant, SiteURL: "https://santos-tech.com"},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:            pool,
		Scheduled:       &ScheduledContactRepo{pool: pool},
		AgentGo:         NewAgentGoClient(modelo.URL, "x", "", nil, nil),
		Sender:          &senderGravador{},
		EvolutionSender: evo,
	})
	w.runReactivations(ctx)

	// Só a conversa livre recebeu mensagem, sem as aspas do modelo.
	var paraCliente []OutboundMessage
	for _, m := range evo.msgs {
		if m.To == "5511900000001" || m.To == "5511900000002" {
			paraCliente = append(paraCliente, m)
		}
	}
	if len(paraCliente) != 1 || paraCliente[0].To != "5511900000001" ||
		paraCliente[0].Content.Text != "Oi Vivian! Combinamos de falar agora no fim do mês. Tudo bem por aí?" {
		t.Fatalf("mensagens ao cliente: %+v", paraCliente)
	}

	// Entrou no histórico da conversa.
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM outbound_message WHERE conversation_id = $1 AND content->>'Text' LIKE 'Oi Vivian!%'`, convLivre).Scan(&n))
	if n != 1 {
		t.Errorf("mensagem de reativação no histórico: %d, queria 1", n)
	}

	// O humano soube das duas: o que foi mandado e o que não foi, e por quê.
	var avisos []string
	for _, tx := range evo.textos {
		if strings.HasPrefix(tx, "5516000000000|") {
			avisos = append(avisos, tx)
		}
	}
	junto := strings.Join(avisos, "\n---\n")
	if len(avisos) != 2 || !strings.Contains(junto, "Reativei* Vivian") || !strings.Contains(junto, "Não reativei sozinho: a conversa está com um humano") {
		t.Errorf("avisos ao admin:\n%s", junto)
	}

	// Os dois saíram da fila.
	must(pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_contacts WHERE conversation_id IN ($1, $2) AND status = 'fired'`, convLivre, convHandoff).Scan(&n))
	if n != 2 {
		t.Errorf("retornos marcados como disparados: %d, queria 2", n)
	}
}
