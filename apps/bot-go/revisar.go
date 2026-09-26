package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// "Conversas para revisar" (spec 2026-09-25-bot-playbook-venda, fase 3).
//
// O hábito que faz o playbook crescer: uma vez por semana, abrir as aulas que
// não viraram matrícula e as conversas que esfriaram, e transformar uma ou
// duas em aprendizado. Aula que não fechou vem primeiro — é a lição mais cara,
// e muitas vezes já tem o "por quê?" anotado no Pós-aula.

const (
	diasParaRevisar = 7
	maxParaRevisar  = 30
)

// ItemRevisar — uma conversa na lista.
type ItemRevisar struct {
	ConversaID string    `json:"conversaId"`
	Nome       string    `json:"nome"`
	Telefone   string    `json:"telefone"`
	Motivo     string    `json:"motivo"` // nao_fechou | morno | frio
	Interesse  string    `json:"interesse,omitempty"`
	Observacao string    `json:"observacao,omitempty"` // o "por quê?" do Pós-aula
	Quando     time.Time `json:"quando"`
}

// precisaRevisar diz se o lead esfriou. "frio" só conta quando houve alguma
// troca (ao menos pediu o preço): quem mandou "oi" e sumiu é ruído, não lição.
func precisaRevisar(q Qualificacao) string {
	switch q.Grau() {
	case GrauMorno:
		return "morno"
	case GrauFrio:
		if q.PedidosDePreco > 0 || q.TurnosRespondendo >= 1 {
			return "frio"
		}
	}
	return ""
}

func conversasParaRevisar(ctx context.Context, pool *pgxpool.Pool, tenant TenantID, desde time.Time) ([]ItemRevisar, error) {
	out := []ItemRevisar{}
	visto := map[string]bool{}

	// 1) Aulas que não fecharam.
	rows, err := pool.Query(ctx, `
		SELECT coalesce(cv.id::text, ''), coalesce(ct.display_name, ''), ar.client_phone,
		       ar.observacao, coalesce(ar.aula_em, ar.marcado_em), coalesce(lq.interesse, ''),
		       coalesce(ci.contact_id::text, '')
		FROM aula_resultado ar
		LEFT JOIN channel_identity ci ON ci.tenant_id = $1 AND ci.external_id = ar.client_phone
		LEFT JOIN contact ct ON ct.id = ci.contact_id
		LEFT JOIN lead_qualificacao lq ON lq.tenant_id = $1 AND lq.contact_id = ci.contact_id
		LEFT JOIN LATERAL (
		  SELECT c.id FROM conversation c WHERE c.channel_identity_id = ci.id
		  ORDER BY c.updated_at DESC LIMIT 1
		) cv ON true
		WHERE ar.tenant_id = $1 AND ar.resultado = 'nao_fechou'
		  AND coalesce(ar.aula_em, ar.marcado_em) >= $2
		ORDER BY coalesce(ar.aula_em, ar.marcado_em) DESC
		LIMIT $3`, tenant, desde, maxParaRevisar)
	if err != nil {
		return nil, fmt.Errorf("revisar: aulas: %w", err)
	}
	for rows.Next() {
		var it ItemRevisar
		var contato string
		if err := rows.Scan(&it.ConversaID, &it.Nome, &it.Telefone, &it.Observacao, &it.Quando, &it.Interesse, &contato); err != nil {
			rows.Close()
			return nil, fmt.Errorf("revisar: aulas: %w", err)
		}
		it.Motivo = "nao_fechou"
		if contato != "" {
			visto[contato] = true
		}
		out = append(out, it)
	}
	rows.Close()

	// 2) Leads que esfriaram (grau calculado aqui, como em todo o bot).
	rows, err = pool.Query(ctx, `
		SELECT lq.contact_id::text, coalesce(cv.id::text, ''), coalesce(ct.display_name, ''),
		       coalesce(ci.external_id, ''), lq.atualizado_em,
		       lq.para_quem, lq.aluno_idade, lq.interesse, lq.ja_faz_curso, lq.disponibilidade,
		       lq.motivacao, lq.motivacao_tipo, lq.preco_informado, lq.aula_marcada,
		       lq.turnos_respondendo, lq.pedidos_de_preco
		FROM lead_qualificacao lq
		JOIN contact ct ON ct.id = lq.contact_id
		LEFT JOIN LATERAL (
		  SELECT ci.id, ci.external_id FROM channel_identity ci
		  WHERE ci.tenant_id = $1 AND ci.contact_id = lq.contact_id LIMIT 1
		) ci ON true
		LEFT JOIN LATERAL (
		  SELECT c.id FROM conversation c WHERE c.channel_identity_id = ci.id
		  ORDER BY c.updated_at DESC LIMIT 1
		) cv ON true
		WHERE lq.tenant_id = $1 AND lq.atualizado_em >= $2
		ORDER BY lq.atualizado_em DESC
		LIMIT 200`, tenant, desde)
	if err != nil {
		return nil, fmt.Errorf("revisar: leads: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var it ItemRevisar
		var contato string
		var q Qualificacao
		if err := rows.Scan(&contato, &it.ConversaID, &it.Nome, &it.Telefone, &it.Quando,
			&q.ParaQuem, &q.AlunoIdade, &q.Interesse, &q.JaFazCurso, &q.Disponibilidade,
			&q.Motivacao, &q.MotivacaoTipo, &q.PrecoInformado, &q.AulaMarcada,
			&q.TurnosRespondendo, &q.PedidosDePreco); err != nil {
			return nil, fmt.Errorf("revisar: leads: %w", err)
		}
		if visto[contato] || len(out) >= maxParaRevisar {
			continue
		}
		if it.Motivo = precisaRevisar(q); it.Motivo == "" {
			continue
		}
		it.Interesse = q.Interesse
		visto[contato] = true
		out = append(out, it)
	}
	return out, rows.Err()
}

// GET /api/vendas/revisar
func (s *Server) handleRevisar(w http.ResponseWriter, r *http.Request) {
	desde := time.Now().AddDate(0, 0, -diasParaRevisar)
	itens, err := conversasParaRevisar(r.Context(), s.pool, s.tenantDoPainel(), desde)
	if err != nil {
		s.logger.Error("revisar", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"itens": itens, "dias": diasParaRevisar})
}
