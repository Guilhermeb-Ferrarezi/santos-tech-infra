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
	heatmapRefWidthDesktop   = 1024
	heatmapRefWidthMobile    = 390
	heatmapCols              = 40  // colunas da grade (eixo X)
	heatmapRows              = 200 // linhas da grade (eixo Y) — medido ao vivo: post de 7431px com 60 linhas dava célula de ~124px, maior que um avatar de ~40px (o ponto renderiza no centro da célula, "quase lá" mas não exato). Com 200, célula de ~37px num post desse tamanho — ainda maior que o ideal em posts MUITO longos, mas ordem de grandeza melhor.
	heatmapMaxClicksPerBatch = 200
)

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

	for _, c := range in.Clicks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO blog_heatmap_clicks (post_slug, viewport, x_pct, y_pct, session_id)
			VALUES ($1,$2,$3,$4,$5)`,
			in.PostSlug, in.Viewport, c.XPct, c.YPct, in.SessionID); err != nil {
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

func (s *Server) blogHeatmapClickGrid(ctx context.Context, postSlug, viewport string, f BlogMetricsFilter) (*BlogHeatmapClickGrid, error) {
	rows, err := s.db.Query(ctx, `
		SELECT LEAST(GREATEST(floor(x_pct * $1::real)::int, 0), $1::int - 1) AS xb,
			LEAST(GREATEST(floor(y_pct * $2::real)::int, 0), $2::int - 1) AS yb,
			count(*) AS cnt
		FROM blog_heatmap_clicks
		WHERE post_slug = $3 AND viewport = $4 AND created_at >= $5 AND created_at < $6
		GROUP BY xb, yb
		ORDER BY yb, xb`,
		heatmapCols, heatmapRows, postSlug, viewport, f.From, f.To)
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
		Cols:           heatmapCols,
		Rows:           heatmapRows,
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
