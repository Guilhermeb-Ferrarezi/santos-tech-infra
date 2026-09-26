package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Indicadores do playbook — "o bot está vendendo melhor?" (spec
// 2026-09-25-bot-playbook-venda §4, aba Indicadores de Como o bot vende).
//
// Tudo sai de dados que já são gravados: o dossiê (lead_qualificacao), o
// resultado da aula (aula_resultado) e, desde o playbook, que fichas o bot usou
// em cada conversa (bot_playbook_uso). Período atual × anterior de mesmo
// tamanho, com as mudanças de regra e de ficha marcadas na linha do tempo.
//
// HONESTIDADE ESTATÍSTICA: a escola tem poucos leads por mês. Abaixo de
// minimoParaPercentual casos, só contagem — "60%" de 3 em 5 engana. E nada
// aqui diz "melhorou POR CAUSA de": o número serve para não piorar e para
// achar conversa a revisar.

const minimoParaPercentual = 20

// Contagem — n de total, com porcentagem só quando há casos suficientes.
// Media vale para os indicadores de média (perguntas por lead).
type Contagem struct {
	N          int      `json:"n"`
	Total      int      `json:"total"`
	Percentual *float64 `json:"percentual"`
	Media      *float64 `json:"media,omitempty"`
}

func novaContagem(n, total int) Contagem {
	c := Contagem{N: n, Total: total}
	if total >= minimoParaPercentual {
		p := math.Round(float64(n)*1000/float64(total)) / 10
		c.Percentual = &p
	}
	return c
}

type indicadoresDeLeads struct {
	qualificados, precoValvula, aulaMarcada, perguntas Contagem
}

// indicadoresDosLeads — o que os dossiês dos leads do período dizem.
//
//	qualificados  — grau qualificado ou muito qualificado, entre todos os leads
//	precoValvula  — souberam o preço pela válvula de escape (pediram 2 vezes
//	                sem conversar), entre os que souberam o preço. APROXIMAÇÃO:
//	                o dossiê não guarda em que momento o preço saiu.
//	aulaMarcada   — marcaram aula, entre os que conversaram (1+ mensagem)
//	perguntas     — média de perguntas-chave respondidas, entre os que conversaram
func indicadoresDosLeads(leads []Qualificacao) indicadoresDeLeads {
	var qualif, sabem, valvula, conversaram, marcaram, respostas int
	for _, q := range leads {
		if g := q.Grau(); g == GrauQualificado || g == GrauMuitoQualificado {
			qualif++
		}
		if q.PrecoInformado {
			sabem++
			if q.PedidosDePreco >= 2 && q.TurnosRespondendo < 2 {
				valvula++
			}
		}
		if q.TurnosRespondendo >= 1 {
			conversaram++
			respostas += q.Respondidas()
			if q.AulaMarcada {
				marcaram++
			}
		}
	}
	out := indicadoresDeLeads{
		qualificados: novaContagem(qualif, len(leads)),
		precoValvula: novaContagem(valvula, sabem),
		aulaMarcada:  novaContagem(marcaram, conversaram),
		perguntas:    Contagem{N: respostas, Total: conversaram},
	}
	if conversaram > 0 {
		m := math.Round(float64(respostas)*100/float64(conversaram)) / 100
		out.perguntas.Media = &m
	}
	return out
}

// indicadorDasAulas — fecharam entre os que vieram (veio + fechou + não
// fechou), e quantas aulas estão sem marcação. "Sem marcação" vai junto de
// propósito: sem ele, 20% pode ser "oito não fecharam" ou "ninguém marcou".
func indicadorDasAulas(resultados []string) (Contagem, int) {
	var vieram, fecharam, sem int
	for _, r := range resultados {
		switch ResultadoAula(r) {
		case ResultadoFechou:
			fecharam++
			vieram++
		case ResultadoVeio, ResultadoNaoFechou:
			vieram++
		case ResultadoFaltou:
		default:
			sem++
		}
	}
	return novaContagem(fecharam, vieram), sem
}

// Periodo — [De, Ate) no fuso da escola, e o anterior colado, de mesmo tamanho.
type Periodo struct {
	De          time.Time `json:"de"`
	Ate         time.Time `json:"ate"` // exclusivo
	AnteriorDe  time.Time `json:"anteriorDe"`
	AnteriorAte time.Time `json:"anteriorAte"`
}

const maxDiasIndicadores = 366

// periodoDosIndicadores lê de/ate (YYYY-MM-DD, "ate" inclusivo). Vazio = os
// últimos 30 dias até hoje.
func periodoDosIndicadores(de, ate string, agora time.Time) (Periodo, error) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		loc = time.UTC
	}
	hoje := agora.In(loc)
	diaDe := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc) }
	fim := diaDe(hoje).AddDate(0, 0, 1)
	if ate != "" {
		t, err := time.ParseInLocation("2006-01-02", ate, loc)
		if err != nil {
			return Periodo{}, fmt.Errorf("data final inválida (use AAAA-MM-DD)")
		}
		fim = t.AddDate(0, 0, 1)
	}
	ini := fim.AddDate(0, 0, -30)
	if de != "" {
		t, err := time.ParseInLocation("2006-01-02", de, loc)
		if err != nil {
			return Periodo{}, fmt.Errorf("data inicial inválida (use AAAA-MM-DD)")
		}
		ini = t
	}
	if !ini.Before(fim) {
		return Periodo{}, fmt.Errorf("a data inicial precisa ser antes da final")
	}
	dias := int(math.Round(fim.Sub(ini).Hours() / 24))
	if dias > maxDiasIndicadores {
		return Periodo{}, fmt.Errorf("o período aceita no máximo %d dias", maxDiasIndicadores)
	}
	return Periodo{De: ini, Ate: fim, AnteriorDe: ini.AddDate(0, 0, -dias), AnteriorAte: ini}, nil
}

// Indicador — um cartão da aba Indicadores.
type Indicador struct {
	Chave    string   `json:"chave"`
	Rotulo   string   `json:"rotulo"`
	Ajuda    string   `json:"ajuda"`
	Tipo     string   `json:"tipo"` // "proporcao" | "media"
	Atual    Contagem `json:"atual"`
	Anterior Contagem `json:"anterior"`
}

// UsoFicha — "usada em N conversas → M marcaram → K fecharam".
type UsoFicha struct {
	SituacaoID string `json:"situacaoId"`
	Titulo     string `json:"titulo"`
	Estado     string `json:"estado"`
	Conversas  int    `json:"conversas"`
	Marcaram   int    `json:"marcaram"`
	Fecharam   int    `json:"fecharam"`
}

// Marca — uma mudança na linha do tempo (regras salvas ou ficha mexida).
type Marca struct {
	Em         time.Time `json:"em"`
	Tipo       string    `json:"tipo"` // "regras" | "ficha"
	Acao       string    `json:"acao"`
	Titulo     string    `json:"titulo,omitempty"`
	Alteracoes []string  `json:"alteracoes,omitempty"`
}

type RespostaIndicadores struct {
	Periodo              Periodo     `json:"periodo"`
	Indicadores          []Indicador `json:"indicadores"`
	SemMarcacao          [2]int      `json:"semMarcacao"` // [atual, anterior]
	UsoPorFicha          []UsoFicha  `json:"usoPorFicha"`
	Marcas               []Marca     `json:"marcas"`
	MinimoParaPercentual int         `json:"minimoParaPercentual"`
}

// ── leitura do banco ─────────────────────────────────────────────────────────

func leadsDoPeriodo(ctx context.Context, pool *pgxpool.Pool, tenant TenantID, de, ate time.Time) ([]Qualificacao, error) {
	rows, err := pool.Query(ctx, `
		SELECT para_quem, aluno_idade, interesse, ja_faz_curso, disponibilidade,
		       motivacao, motivacao_tipo, preco_informado, aula_marcada,
		       turnos_respondendo, pedidos_de_preco
		FROM lead_qualificacao
		WHERE tenant_id = $1 AND criado_em >= $2 AND criado_em < $3`, tenant, de, ate)
	if err != nil {
		return nil, fmt.Errorf("indicadores: leads: %w", err)
	}
	defer rows.Close()
	var out []Qualificacao
	for rows.Next() {
		var q Qualificacao
		if err := rows.Scan(&q.ParaQuem, &q.AlunoIdade, &q.Interesse, &q.JaFazCurso, &q.Disponibilidade,
			&q.Motivacao, &q.MotivacaoTipo, &q.PrecoInformado, &q.AulaMarcada,
			&q.TurnosRespondendo, &q.PedidosDePreco); err != nil {
			return nil, fmt.Errorf("indicadores: leads: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// resultadosDoPeriodo — o desfecho de cada aula marcada pelo bot que já
// aconteceu no período (” = ninguém marcou ainda). Mesma lista do Pós-aula.
func resultadosDoPeriodo(ctx context.Context, pool *pgxpool.Pool, tenant TenantID, de, ate time.Time) ([]string, error) {
	rows, err := pool.Query(ctx, `
		WITH aulas AS (
			SELECT DISTINCT ON (notion_page_id) notion_page_id, aula_em
			FROM booking_reminder
			WHERE tenant_id = $1 AND status <> 'cancelado'
			ORDER BY notion_page_id, aula_em
		)
		SELECT coalesce(ar.resultado, '')
		FROM aulas a
		LEFT JOIN aula_resultado ar ON ar.tenant_id = $1 AND ar.notion_page_id = a.notion_page_id
		WHERE a.aula_em >= $2 AND a.aula_em < $3 AND a.aula_em < now()`, tenant, de, ate)
	if err != nil {
		return nil, fmt.Errorf("indicadores: aulas: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, fmt.Errorf("indicadores: aulas: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func usoPorFicha(ctx context.Context, pool *pgxpool.Pool, tenant TenantID, de, ate time.Time) ([]UsoFicha, error) {
	rows, err := pool.Query(ctx, `
		SELECT s.id::text, s.titulo, s.estado,
		       count(DISTINCT u.conversation_id),
		       count(DISTINCT u.contact_id) FILTER (WHERE lq.aula_marcada),
		       count(DISTINCT u.contact_id) FILTER (WHERE EXISTS (
		         SELECT 1 FROM aula_resultado ar
		         JOIN channel_identity ci ON ci.tenant_id = $1 AND ci.external_id = ar.client_phone
		         WHERE ar.tenant_id = $1 AND ar.resultado = 'fechou' AND ci.contact_id = u.contact_id))
		FROM bot_playbook_situacao s
		LEFT JOIN bot_playbook_uso u
		       ON u.tenant_id = $1 AND u.situacao_id = s.id AND u.usado_em >= $2 AND u.usado_em < $3
		LEFT JOIN lead_qualificacao lq ON lq.tenant_id = $1 AND lq.contact_id = u.contact_id
		WHERE s.tenant_id = $1
		GROUP BY s.id, s.titulo, s.estado
		HAVING s.estado = 'ativa' OR count(u.situacao_id) > 0
		ORDER BY count(DISTINCT u.conversation_id) DESC, s.titulo`, tenant, de, ate)
	if err != nil {
		return nil, fmt.Errorf("indicadores: uso por ficha: %w", err)
	}
	defer rows.Close()
	out := []UsoFicha{}
	for rows.Next() {
		var u UsoFicha
		if err := rows.Scan(&u.SituacaoID, &u.Titulo, &u.Estado, &u.Conversas, &u.Marcaram, &u.Fecharam); err != nil {
			return nil, fmt.Errorf("indicadores: uso por ficha: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func marcasDoPeriodo(ctx context.Context, pool *pgxpool.Pool, tenant TenantID, de, ate time.Time) ([]Marca, error) {
	rows, err := pool.Query(ctx, `
		SELECT em, tipo, acao, titulo, alteracoes FROM (
		  SELECT salvo_em AS em, 'regras' AS tipo,
		         CASE WHEN restaurada_de IS NULL THEN 'salvou' ELSE 'restaurou' END AS acao,
		         '' AS titulo, alteracoes
		  FROM bot_regras_venda_versao WHERE tenant_id = $1 AND salvo_em >= $2 AND salvo_em < $3
		  UNION ALL
		  SELECT e.em, 'ficha', e.acao, s.titulo, '{}'::text[]
		  FROM bot_playbook_evento e JOIN bot_playbook_situacao s ON s.id = e.situacao_id
		  WHERE e.tenant_id = $1 AND e.em >= $2 AND e.em < $3
		) m ORDER BY em DESC LIMIT 100`, tenant, de, ate)
	if err != nil {
		return nil, fmt.Errorf("indicadores: marcas: %w", err)
	}
	defer rows.Close()
	out := []Marca{}
	for rows.Next() {
		var m Marca
		if err := rows.Scan(&m.Em, &m.Tipo, &m.Acao, &m.Titulo, &m.Alteracoes); err != nil {
			return nil, fmt.Errorf("indicadores: marcas: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func montaIndicadores(ctx context.Context, pool *pgxpool.Pool, tenant TenantID, p Periodo) (RespostaIndicadores, error) {
	leadsAtual, err := leadsDoPeriodo(ctx, pool, tenant, p.De, p.Ate)
	if err != nil {
		return RespostaIndicadores{}, err
	}
	leadsAntes, err := leadsDoPeriodo(ctx, pool, tenant, p.AnteriorDe, p.AnteriorAte)
	if err != nil {
		return RespostaIndicadores{}, err
	}
	aulasAtual, err := resultadosDoPeriodo(ctx, pool, tenant, p.De, p.Ate)
	if err != nil {
		return RespostaIndicadores{}, err
	}
	aulasAntes, err := resultadosDoPeriodo(ctx, pool, tenant, p.AnteriorDe, p.AnteriorAte)
	if err != nil {
		return RespostaIndicadores{}, err
	}
	uso, err := usoPorFicha(ctx, pool, tenant, p.De, p.Ate)
	if err != nil {
		return RespostaIndicadores{}, err
	}
	// As marcas cobrem os dois períodos: a mudança que separa um do outro é
	// justamente a que interessa.
	marcas, err := marcasDoPeriodo(ctx, pool, tenant, p.AnteriorDe, p.Ate)
	if err != nil {
		return RespostaIndicadores{}, err
	}

	la, lb := indicadoresDosLeads(leadsAtual), indicadoresDosLeads(leadsAntes)
	fa, semA := indicadorDasAulas(aulasAtual)
	fb, semB := indicadorDasAulas(aulasAntes)
	return RespostaIndicadores{
		Periodo: p,
		Indicadores: []Indicador{
			{"qualificados", "Leads qualificados", "Conversaram antes de marcar a aula (grau qualificado ou muito qualificado), entre todos os leads novos.", "proporcao", la.qualificados, lb.qualificados},
			{"precoValvula", "Preço antes da conversa", "Souberam o preço porque pediram duas vezes sem responder as perguntas, entre os que souberam o preço. Quanto menor, melhor. É uma aproximação.", "proporcao", la.precoValvula, lb.precoValvula},
			{"perguntas", "Perguntas respondidas por lead", "Média das perguntas-chave (para quem, idade, interesse…) respondidas por quem conversou.", "media", la.perguntas, lb.perguntas},
			{"aulaMarcada", "Aula experimental marcada", "Marcaram a aula, entre os leads que conversaram.", "proporcao", la.aulaMarcada, lb.aulaMarcada},
			{"fechouVeio", "Fecharam, entre os que vieram", "Das aulas que já aconteceram, quantos fecharam entre os que apareceram (marcado no Pós-aula).", "proporcao", fa, fb},
		},
		SemMarcacao:          [2]int{semA, semB},
		UsoPorFicha:          uso,
		Marcas:               marcas,
		MinimoParaPercentual: minimoParaPercentual,
	}, nil
}

// GET /api/vendas/indicadores?de=AAAA-MM-DD&ate=AAAA-MM-DD
func (s *Server) handleIndicadores(w http.ResponseWriter, r *http.Request) {
	p, err := periodoDosIndicadores(r.URL.Query().Get("de"), r.URL.Query().Get("ate"), time.Now())
	if err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := montaIndicadores(r.Context(), s.pool, s.tenantDoPainel(), p)
	if err != nil {
		s.logger.Error("indicadores", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, res)
}
