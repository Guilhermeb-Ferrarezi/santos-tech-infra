package main

import "context"

// ── Models ───────────────────────────────────────────────────────────────────

// heatmapRefWidthDesktop/Mobile são as larguras de referência do PREVIEW no
// admin (iframe same-origin do post real, ver dashboard/web/src/pages/admin/
// blog/Heatmap.tsx). Não afetam a captura nem a agregação: x_pct e y_pct são
// SEMPRE porcentagem (0..1) relativa ao container real no momento do clique,
// nos dois eixos — resolução-independente por design. Isso importa porque
// acima do breakpoint `lg:` (1024px, onde o post ganha a sidebar de índice em
// grid) a largura do conteúdo continua variando (não trava num valor fixo);
// se Y fosse pixel absoluto, cliques capturados numa tela de 1920px não
// bateriam com o preview renderizado a 1024px. Corte mobile/desktop no client
// (blog/web/src/lib/blog-heatmap.ts, MOBILE_BREAKPOINT_PX) tem que continuar
// igual a esse valor, senão captura e agregação saem de sincronia.
const (
	heatmapRefWidthDesktop = 1024
	heatmapRefWidthMobile  = 390
	// heatmapDefaultCols/Rows só valem quando o chamador não manda cols/rows
	// na query (ex.: teste manual da API) — o admin de verdade sempre manda
	// os dois, calculados a partir do tamanho REAL medido do post renderizado
	// (ver dashboard/web/.../Heatmap.tsx, TARGET_CELL_PX), porque um número
	// fixo nunca acompanha a variação real de altura entre posts (medido ao
	// vivo: um post de 7431px com 60 linhas dava célula de ~124px, maior que
	// um avatar de ~40px — o ponto renderiza no centro da célula, então
	// "quase lá" mas visualmente fora do alvo).
	heatmapDefaultCols       = 30
	heatmapDefaultRows       = 60
	heatmapMinGridCells      = 10  // menos que isso e a grade fica grossa demais pra ser útil
	heatmapMaxGridCells      = 400 // mais que isso e o GROUP BY fica caro à toa (grade mais fina que 1 clique de diferença)
	heatmapMaxClicksPerBatch = 200
)

// clampHeatmapGridSize satura n em [heatmapMinGridCells, heatmapMaxGridCells]
// — nunca confia em cols/rows vindos da query string sem limite (não é input
// de usuário final, mas nada impede um valor absurdo por engano/teste).
func clampHeatmapGridSize(n int) int {
	if n < heatmapMinGridCells {
		return heatmapMinGridCells
	}
	if n > heatmapMaxGridCells {
		return heatmapMaxGridCells
	}
	return n
}

var validHeatmapViewports = map[string]bool{"mobile": true, "desktop": true}

func heatmapReferenceWidth(viewport string) int {
	if viewport == "mobile" {
		return heatmapRefWidthMobile
	}
	return heatmapRefWidthDesktop
}

type HeatmapClickInput struct {
	XPct float64 `json:"xPct"`
	YPct float64 `json:"yPct"`
}

// BlogHeatmapBatch é o corpo do beacon — sempre um lote (cliques acumulados +
// no máximo 1 profundidade de scroll), nunca 1 request por clique.
type BlogHeatmapBatch struct {
	PostSlug          string              `json:"postSlug"`
	Viewport          string              `json:"viewport"`
	SessionID         string              `json:"sessionId"`
	Clicks            []HeatmapClickInput `json:"clicks"`
	MaxScrollDepthPct *float64            `json:"maxScrollDepthPct"`
}

type BlogHeatmapClickBucket struct {
	XBucket int   `json:"xBucket"`
	YBucket int   `json:"yBucket"`
	Count   int64 `json:"count"`
}

// BlogHeatmapClickGrid nunca expõe pontos crus — só a grade já binada. Cols/
// Rows dizem ao admin como converter bucket -> fração (bucket+0.5)/cols (ou
// /rows), que ele multiplica pela largura/altura REAL do preview renderizado
// — nunca por ReferenceWidth diretamente, isso é só informativo.
type BlogHeatmapClickGrid struct {
	ReferenceWidth int                      `json:"referenceWidth"`
	Cols           int                      `json:"cols"`
	Rows           int                      `json:"rows"`
	Buckets        []BlogHeatmapClickBucket `json:"buckets"`
}

// BlogHeatmapScrollDecile é 1 linha de funil: de 0 a 1 sessões (Total) que
// chegaram até pelo menos `Decile` (0.1..1.0) de profundidade do post.
type BlogHeatmapScrollDecile struct {
	Decile  float64 `json:"decile"`
	Reached int64   `json:"reached"`
	Total   int64   `json:"total"`
}

// ── Store — ingestão ─────────────────────────────────────────────────────────

func (s *Server) insertHeatmapBatch(ctx context.Context, in BlogHeatmapBatch) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Um INSERT em lote (unnest) no lugar de um round-trip por clique — mesmo
	// padrão já usado em portal_content_store.go/social.go/task.go. Rota
	// pública de alta frequência (1 beacon por pageview, até
	// heatmapMaxClicksPerBatch=200 cliques cada): o loop anterior fazia até
	// 200 INSERTs sequenciais na mesma transação por request.
	if len(in.Clicks) > 0 {
		xs := make([]float32, len(in.Clicks))
		ys := make([]float32, len(in.Clicks))
		for i, c := range in.Clicks {
			xs[i] = float32(c.XPct)
			ys[i] = float32(c.YPct)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO blog_heatmap_clicks (post_slug, viewport, x_pct, y_pct, session_id)
			SELECT $1, $2, t.xp, t.yp, $5 FROM unnest($3::real[], $4::real[]) AS t(xp, yp)`,
			in.PostSlug, in.Viewport, xs, ys, in.SessionID); err != nil {
			return err
		}
	}
	if in.MaxScrollDepthPct != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO blog_heatmap_scroll (post_slug, viewport, max_depth_pct, session_id)
			VALUES ($1,$2,$3,$4)`,
			in.PostSlug, in.Viewport, *in.MaxScrollDepthPct, in.SessionID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ── Store — agregação ────────────────────────────────────────────────────────

// gridCols/gridRows já vêm resolvidos e saturados pelo handler (ver
// gridDimFromQuery em handlers_blog_heatmap.go) — nunca 0 nem fora dos
// limites [heatmapMinGridCells, heatmapMaxGridCells] neste ponto.
func (s *Server) blogHeatmapClickGrid(ctx context.Context, postSlug, viewport string, gridCols, gridRows int, f BlogMetricsFilter) (*BlogHeatmapClickGrid, error) {
	rows, err := s.db.Query(ctx, `
		SELECT LEAST(GREATEST(floor(x_pct * $1::real)::int, 0), $1::int - 1) AS xb,
			LEAST(GREATEST(floor(y_pct * $2::real)::int, 0), $2::int - 1) AS yb,
			count(*) AS cnt
		FROM blog_heatmap_clicks
		WHERE post_slug = $3 AND viewport = $4 AND created_at >= $5 AND created_at < $6
		GROUP BY xb, yb
		ORDER BY yb, xb`,
		gridCols, gridRows, postSlug, viewport, f.From, f.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := []BlogHeatmapClickBucket{}
	for rows.Next() {
		var b BlogHeatmapClickBucket
		if err := rows.Scan(&b.XBucket, &b.YBucket, &b.Count); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &BlogHeatmapClickGrid{
		ReferenceWidth: heatmapReferenceWidth(viewport),
		Cols:           gridCols,
		Rows:           gridRows,
		Buckets:        buckets,
	}, nil
}

// blogHeatmapScrollFunnel devolve 10 decis (0.1..1.0): quantas sessões (de-dup
// por session_id, pegando a profundidade MÁXIMA de cada uma) chegaram até
// pelo menos aquele ponto do post.
func (s *Server) blogHeatmapScrollFunnel(ctx context.Context, postSlug, viewport string, f BlogMetricsFilter) ([]BlogHeatmapScrollDecile, error) {
	rows, err := s.db.Query(ctx, `
		WITH session_depth AS (
			SELECT session_id, max(max_depth_pct) AS depth
			FROM blog_heatmap_scroll
			WHERE post_slug = $1 AND viewport = $2 AND created_at >= $3 AND created_at < $4
			GROUP BY session_id
		)
		SELECT gs.decile,
			(SELECT count(*) FROM session_depth sd WHERE sd.depth >= gs.decile) AS reached,
			(SELECT count(*) FROM session_depth) AS total
		FROM generate_series(0.1, 1.0, 0.1) AS gs(decile)
		ORDER BY gs.decile`,
		postSlug, viewport, f.From, f.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []BlogHeatmapScrollDecile{}
	for rows.Next() {
		var d BlogHeatmapScrollDecile
		if err := rows.Scan(&d.Decile, &d.Reached, &d.Total); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// deleteOldBlogHeatmapEvents apaga cliques/scroll com mais de 180 dias —
// mesma janela de retenção do resto do analytics do blog (ver
// deleteOldBlogEvents em blog_analytics.go).
func (s *Server) deleteOldBlogHeatmapEvents(ctx context.Context) (int64, error) {
	clicksTag, err := s.db.Exec(ctx, `DELETE FROM blog_heatmap_clicks WHERE created_at < now() - interval '180 days'`)
	if err != nil {
		return 0, err
	}
	scrollTag, err := s.db.Exec(ctx, `DELETE FROM blog_heatmap_scroll WHERE created_at < now() - interval '180 days'`)
	if err != nil {
		return clicksTag.RowsAffected(), err
	}
	return clicksTag.RowsAffected() + scrollTag.RowsAffected(), nil
}
