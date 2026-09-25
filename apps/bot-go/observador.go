package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Modo observador — fase 4 do follow-up (spec dashboard/docs/superpowers/specs/
// 2026-09-25-bot-follow-up-reativacao-design.md, item 5; aprovado em 25/09).
//
// Com um humano atendendo, o bot não lia nada: no número oficial o engine sai
// antes do modelo quando o bot está desligado na conversa, e no Evolution com o
// bot desligado só o lead é capturado. Um "me chama em dezembro" dito ao Rodrigo
// sumia. O observador lê a mensagem do CLIENTE, sem responder, e registra:
//
//   - pedido de retorno com data → a mesma fila das fases 1–3 (o responsável é
//     avisado no dia);
//   - compromisso sem data ("vou falar com meu marido") → lead_qualificacao.
//     compromissos, que aparece no card do CRM e no que o bot sabe da pessoa.
//
// Liga/desliga em WhatsApp · Configurações. Custo: uma consulta curta por
// mensagem de cliente com 3 palavras ou mais — "ok" e "obrigada" não gastam.

const (
	observadorMinPalavras    = 3
	observadorMaxDias        = 400 // mais longe que isto é data inventada pelo modelo
	observadorJanelaDedup    = 3   // dias: retorno pedido de novo perto da mesma data não duplica
	compromissoMaxRunes      = 200
	observadorFraseMaxRunes  = 300
	confiancaPadraoObservada = 0.5
)

var diasDaSemanaPT = [...]string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"}

// deveObservar — o filtro barato antes de gastar uma consulta.
func deveObservar(texto string) bool {
	return len(strings.Fields(texto)) >= observadorMinPalavras
}

func promptDoObservador(texto, resumo string, hoje time.Time) string {
	h := hoje.In(brLocation)
	var b strings.Builder
	fmt.Fprintf(&b, "Hoje é %s (%s), fuso de Brasília.\n\n", h.Format("2006-01-02"), diasDaSemanaPT[h.Weekday()])
	b.WriteString("Você está LENDO, sem participar, uma conversa de WhatsApp entre um cliente e a equipe de uma escola de tecnologia. NÃO responda o cliente.\n\n")
	if resumo != "" {
		fmt.Fprintf(&b, "Resumo do cliente até aqui: %s\n\n", resumo)
	}
	fmt.Fprintf(&b, "Mensagem do cliente: \"%s\"\n\n", texto)
	b.WriteString("Responda SÓ com um JSON neste formato:\n")
	b.WriteString(`{"retorno": {"data": "AAAA-MM-DD", "confianca": 0.0} ou null, "compromisso": "texto curto" ou null}` + "\n\n")
	b.WriteString("- \"retorno\": só se o cliente pediu ou combinou ser procurado numa data (\"me chama dia 10\", \"em dezembro consigo mais\", \"te falo segunda\"). Converta para uma data futura; mês sem dia = dia 1. \"confianca\" de 0 a 1: 0.9 com dia exato, 0.5 com data vaga.\n")
	b.WriteString("- \"compromisso\": algo que o cliente disse que vai fazer ou decidir, SEM data (\"vou falar com meu marido\", \"vou ver a agenda do meu filho\"). Até 15 palavras, na terceira pessoa.\n")
	b.WriteString("- Nada disso na mensagem: {\"retorno\": null, \"compromisso\": null}. Não invente.\n")
	return b.String()
}

type retornoObservado struct {
	Data      string  `json:"data"`
	Confianca float64 `json:"confianca"`
}

type observacao struct {
	Retorno     *retornoObservado
	Compromisso string
}

// parseObservacao lê o JSON da resposta, tolerando texto e cercas em volta.
func parseObservacao(raw string) (observacao, bool) {
	i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if i < 0 || j <= i {
		return observacao{}, false
	}
	var v struct {
		Retorno     *retornoObservado `json:"retorno"`
		Compromisso *string           `json:"compromisso"`
	}
	if err := json.Unmarshal([]byte(raw[i:j+1]), &v); err != nil {
		return observacao{}, false
	}
	o := observacao{Retorno: v.Retorno}
	if v.Compromisso != nil {
		o.Compromisso = strings.TrimSpace(*v.Compromisso)
	}
	return o, true
}

// dataDoRetornoObservado aceita só data futura (a partir de amanhã) e até
// observadorMaxDias. Hoje não conta: a pessoa já está falando com alguém.
func dataDoRetornoObservado(data string, hoje time.Time) (time.Time, bool) {
	d, err := time.ParseInLocation("2006-01-02", data, brLocation)
	if err != nil {
		return time.Time{}, false
	}
	h := hoje.In(brLocation)
	hojeDia := time.Date(h.Year(), h.Month(), h.Day(), 0, 0, 0, 0, brLocation)
	if !d.After(hojeDia) || d.After(hojeDia.AddDate(0, 0, observadorMaxDias)) {
		return time.Time{}, false
	}
	return d, true
}

func linhaDeCompromisso(texto string, hoje time.Time) string {
	t := strings.Join(strings.Fields(texto), " ")
	if t == "" {
		return ""
	}
	linha := fmt.Sprintf("Compromisso (%s): %s", hoje.In(brLocation).Format("02/01"), t)
	if r := []rune(linha); len(r) > compromissoMaxRunes {
		linha = string(r[:compromissoMaxRunes-1]) + "…"
	}
	return linha
}

// ObservadorRepo grava o que o observador encontrou.
type ObservadorRepo struct{ pool *pgxpool.Pool }

func NewObservadorRepo(pool *pgxpool.Pool) *ObservadorRepo { return &ObservadorRepo{pool: pool} }

// Resumo do lead, para dar contexto à consulta.
func (r *ObservadorRepo) Resumo(ctx context.Context, tenant TenantID, contact ContactID) string {
	var s string
	_ = r.pool.QueryRow(ctx, `SELECT coalesce(summary, '') FROM lead_summary WHERE tenant_id = $1 AND contact_id = $2`, tenant, contact).Scan(&s)
	return s
}

// RegistraRetorno põe o pedido na fila de retornos, às 9h de Brasília do dia.
// Não duplica: com um retorno pendente do mesmo contato a observadorJanelaDedup
// dias dessa data, não cria outro. Devolve se criou.
func (r *ObservadorRepo) RegistraRetorno(ctx context.Context, tenant TenantID, contact ContactID, conv ConversationID, dia time.Time, frase string, confianca float64) (bool, error) {
	payload, _ := json.Marshal(map[string]any{
		"kind":       KindRetorno,
		"rawPhrase":  truncaRunes(frase, observadorFraseMaxRunes),
		"confidence": confianca,
		"origem":     "observador",
	})
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO scheduled_contacts (tenant_id, contact_id, conversation_id, fire_at, status, payload)
		SELECT $1, $2, $3, ($4::date + time '09:00') AT TIME ZONE 'America/Sao_Paulo', 'pending', $5::jsonb
		WHERE NOT EXISTS (
			SELECT 1 FROM scheduled_contacts
			WHERE tenant_id = $1 AND contact_id = $2
			  AND payload->>'kind' = 'reactivation' AND status = 'pending'
			  AND fire_at >= (($4::date - $6::int) + time '00:00') AT TIME ZONE 'America/Sao_Paulo'
			  AND fire_at <  (($4::date + $6::int + 1) + time '00:00') AT TIME ZONE 'America/Sao_Paulo'
		)
	`, tenant, contact, conv, dia.Format("2006-01-02"), string(payload), observadorJanelaDedup)
	if err != nil {
		return false, fmt.Errorf("ObservadorRepo.RegistraRetorno: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RegistraCompromisso acrescenta a linha em lead_qualificacao.compromissos.
// Coluna própria de propósito: `observacoes` é regravada pelo bot a partir do
// que ele leu no começo do atendimento (qualificacao.go), e uma linha gravada
// por fora nesse meio-tempo se perderia.
// Não repete um compromisso com o mesmo texto.
func (r *ObservadorRepo) RegistraCompromisso(ctx context.Context, tenant TenantID, contact ContactID, linha, texto string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO lead_qualificacao (tenant_id, contact_id, compromissos, atualizado_em)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (tenant_id, contact_id) DO UPDATE SET
		  compromissos = CASE WHEN lead_qualificacao.compromissos = '' THEN $3
		                      ELSE lead_qualificacao.compromissos || E'\n' || $3 END,
		  atualizado_em = now()
		WHERE position(lower($4) in lower(lead_qualificacao.compromissos)) = 0
	`, tenant, contact, linha, strings.Join(strings.Fields(texto), " "))
	if err != nil {
		return false, fmt.Errorf("ObservadorRepo.RegistraCompromisso: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Observador faz a consulta e grava. Nunca responde ao cliente; erro só vai
// para o log — ele é um extra, não pode atrapalhar quem está atendendo.
type Observador struct {
	repo   *ObservadorRepo
	agent  *AgentGoClient
	logger *slog.Logger
	agora  func() time.Time
}

func NewObservador(repo *ObservadorRepo, agent *AgentGoClient, logger *slog.Logger) *Observador {
	return &Observador{repo: repo, agent: agent, logger: logger, agora: time.Now}
}

func (o *Observador) Observa(ctx context.Context, tenant TenantID, contact ContactID, conv ConversationID, texto string) {
	if o == nil || o.repo == nil || o.agent == nil || !deveObservar(texto) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	agora := o.agora()
	o.logger.Info("observador: consulta", "conv", conv)
	raw, err := o.agent.RespondWithModel(ctx, promptDoObservador(texto, o.repo.Resumo(ctx, tenant, contact), agora), "haiku", false)
	if err != nil {
		o.logger.Warn("observador: consulta falhou", "err", err)
		return
	}
	obs, ok := parseObservacao(raw)
	if !ok {
		o.logger.Warn("observador: resposta sem JSON válido")
		return
	}
	if obs.Retorno != nil {
		if dia, ok := dataDoRetornoObservado(obs.Retorno.Data, agora); ok {
			conf := obs.Retorno.Confianca
			if conf <= 0 || conf > 1 {
				conf = confiancaPadraoObservada
			}
			criou, err := o.repo.RegistraRetorno(ctx, tenant, contact, conv, dia, texto, conf)
			if err != nil {
				o.logger.Error("observador: não gravou o retorno", "err", err)
			} else if criou {
				o.logger.Info("observador: retorno registrado", "conv", conv, "dia", dia.Format("2006-01-02"))
			}
		}
	}
	if linha := linhaDeCompromisso(obs.Compromisso, agora); linha != "" {
		if _, err := o.repo.RegistraCompromisso(ctx, tenant, contact, linha, obs.Compromisso); err != nil {
			o.logger.Error("observador: não gravou o compromisso", "err", err)
		}
	}
}

// observaEvolution — o gancho do número não-oficial com o bot desligado. Acha
// (ou cria, com o bot desligado) a conversa, para o retorno ter link no aviso.
func (s *Server) observaEvolution(ctx context.Context, phone, texto string) {
	if s.observador == nil || !deveObservar(texto) {
		return
	}
	tenant := TenantID(s.cfg.TenantID)
	cfg, err := NewTenantConfigRepo(s.pool).Get(ctx, nil, tenant)
	if err != nil || !cfg.ObservadorLigado || cfg.IsAdminNumber(phone) {
		return
	}
	var contato ContactID
	var conversa ConversationID
	convs := NewConversationRepo(s.pool)
	err = s.withTenant(ctx, func(tx pgx.Tx) error {
		contact, ident, err := s.contacts.FindByChannelIdentity(ctx, tx, "evolution", phone)
		if err != nil {
			return err
		}
		if contact == nil || ident == nil {
			return fmt.Errorf("contato %s não encontrado", phone)
		}
		c, err := convs.FindByChannelIdentity(ctx, tx, ident.ID)
		if err != nil {
			return err
		}
		if c == nil {
			if c, err = convs.Create(ctx, tx, tenant, contact.ID, ident.ID, "evolution", false); err != nil {
				return err
			}
		}
		contato, conversa = contact.ID, c.ID
		return nil
	})
	if err != nil {
		s.logger.Warn("observador: sem conversa para observar", "err", err)
		return
	}
	s.observador.Observa(ctx, tenant, contato, conversa, texto)
}
