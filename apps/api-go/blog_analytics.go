package main

import (
	"context"
	"time"
)

// ── Models ───────────────────────────────────────────────────────────────────

type BlogEventInput struct {
	Type      string  `json:"type"`
	Path      string  `json:"path"`
	PostSlug  *string `json:"postSlug"`
	SessionID string  `json:"sessionId"`
	VisitorID string  `json:"visitorId"`
	Referrer  string  `json:"referrer"`
	UTMSource string  `json:"utmSource"`
}

var validBlogEventTypes = map[string]bool{"pageview": true, "cta_click": true}

type BlogMetricsOverview struct {
	Pageviews          int64   `json:"pageviews"`
	Visitors           int64   `json:"visitors"`
	CTAClicks          int64   `json:"ctaClicks"`
	ConversionRate     float64 `json:"conversionRate"`
	PrevPageviews      int64   `json:"prevPageviews"`
	PrevVisitors       int64   `json:"prevVisitors"`
	PrevCTAClicks      int64   `json:"prevCtaClicks"`
	PrevConversionRate float64 `json:"prevConversionRate"`
}

type BlogMetricsTimeseriesPoint struct {
	Bucket    time.Time `json:"bucket"`
	Pageviews int64     `json:"pageviews"`
}

type BlogMetricsTopPost struct {
	PostSlug       string  `json:"postSlug"`
	Title          string  `json:"title"`
	Views          int64   `json:"views"`
	CTAClicks      int64   `json:"ctaClicks"`
	ConversionRate float64 `json:"conversionRate"`
}

type BlogMetricsCount struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// BlogMetricsFilter é comum a todos os endpoints de agregação: intervalo de
// tempo obrigatório, post opcional (nil = todos). Referrer/UTMSource/Device/
// Country vêm do drill-down do painel (clicar num item de qualquer ranking
// filtra os outros endpoints por aquele valor) — todos nil = sem filtro.
type BlogMetricsFilter struct {
	From      time.Time
	To        time.Time
	PostSlug  *string
	Referrer  *string
	UTMSource *string
	Device    *string
	Country   *string
}

// ── Store — ingestão ─────────────────────────────────────────────────────────

func (s *Server) insertBlogEvent(ctx context.Context, in BlogEventInput, ua string, country string) error {
	info := parseUserAgent(ua)
	domain := referrerDomain(in.Referrer)
	var referrer *string
	if domain != "" {
		referrer = &domain
	}
	var utm *string
	if in.UTMSource != "" {
		utm = &in.UTMSource
	}
	var browser, os_, ctry *string
	if info.Browser != "" {
		browser = &info.Browser
	}
	if info.OS != "" {
		os_ = &info.OS
	}
	if country != "" {
		ctry = &country
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO blog_events (type, post_slug, path, session_id, visitor_id, referrer, utm_source, device, browser, os, country)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		in.Type, in.PostSlug, in.Path, in.SessionID, in.VisitorID, referrer, utm, info.Device, browser, os_, ctry)
	return err
}

// ── Store — agregação ────────────────────────────────────────────────────────

const blogOverviewSQL = `
SELECT
	count(*) FILTER (WHERE type='pageview') AS pageviews,
	count(DISTINCT visitor_id) AS visitors,
	count(*) FILTER (WHERE type='cta_click') AS cta_clicks
FROM blog_events
WHERE created_at >= $1 AND created_at < $2
	AND ($3::text IS NULL OR post_slug = $3)
	AND ($4::text IS NULL OR referrer = $4)
	AND ($5::text IS NULL OR utm_source = $5)
	AND ($6::text IS NULL OR device = $6)
	AND ($7::text IS NULL OR country = $7)`

func (s *Server) blogMetricsOverview(ctx context.Context, f BlogMetricsFilter) (*BlogMetricsOverview, error) {
	var out BlogMetricsOverview
	if err := s.db.QueryRow(ctx, blogOverviewSQL, f.From, f.To, f.PostSlug, f.Referrer, f.UTMSource, f.Device, f.Country).
		Scan(&out.Pageviews, &out.Visitors, &out.CTAClicks); err != nil {
		return nil, err
	}
	if out.Visitors > 0 {
		out.ConversionRate = float64(out.CTAClicks) / float64(out.Visitors)
	}

	// Período anterior de mesma duração, pra comparação (ex.: 7 dias antes dos
	// 7 dias selecionados).
	dur := f.To.Sub(f.From)
	prevFrom := f.From.Add(-dur)
	prevTo := f.From
	if err := s.db.QueryRow(ctx, blogOverviewSQL, prevFrom, prevTo, f.PostSlug, f.Referrer, f.UTMSource, f.Device, f.Country).
		Scan(&out.PrevPageviews, &out.PrevVisitors, &out.PrevCTAClicks); err != nil {
		return nil, err
	}
	if out.PrevVisitors > 0 {
		out.PrevConversionRate = float64(out.PrevCTAClicks) / float64(out.PrevVisitors)
	}
	return &out, nil
}

func (s *Server) blogMetricsTimeseries(ctx context.Context, f BlogMetricsFilter) ([]BlogMetricsTimeseriesPoint, error) {
	unit := "day"
	if f.To.Sub(f.From) <= 24*time.Hour {
		unit = "hour"
	}
	sql := `
		SELECT gs.bucket, count(be.id)
		FROM generate_series(date_trunc($4, $1::timestamptz), date_trunc($4, $2::timestamptz), ('1 ' || $4)::interval) AS gs(bucket)
		LEFT JOIN blog_events be
			ON be.type = 'pageview'
			AND date_trunc($4, be.created_at) = gs.bucket
			AND be.created_at >= $1 AND be.created_at < $2
			AND ($3::text IS NULL OR be.post_slug = $3)
			AND ($5::text IS NULL OR be.referrer = $5)
			AND ($6::text IS NULL OR be.utm_source = $6)
			AND ($7::text IS NULL OR be.device = $7)
			AND ($8::text IS NULL OR be.country = $8)
		GROUP BY gs.bucket
		ORDER BY gs.bucket`
	rows, err := s.db.Query(ctx, sql, f.From, f.To, f.PostSlug, unit, f.Referrer, f.UTMSource, f.Device, f.Country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BlogMetricsTimeseriesPoint{}
	for rows.Next() {
		var p BlogMetricsTimeseriesPoint
		if err := rows.Scan(&p.Bucket, &p.Pageviews); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Server) blogMetricsTopPosts(ctx context.Context, f BlogMetricsFilter) ([]BlogMetricsTopPost, error) {
	// LEFT JOIN: post pode ter sido apagado depois do evento — nesse caso
	// bp.title é NULL e caímos de volta pro slug (COALESCE).
	rows, err := s.db.Query(ctx, `
		SELECT be.post_slug, COALESCE(bp.title, be.post_slug),
			count(*) FILTER (WHERE be.type='pageview') AS views,
			count(*) FILTER (WHERE be.type='cta_click') AS cta_clicks
		FROM blog_events be
		LEFT JOIN blog_posts bp ON bp.slug = be.post_slug
		WHERE be.created_at >= $1 AND be.created_at < $2 AND be.post_slug IS NOT NULL
			AND ($3::text IS NULL OR be.referrer = $3)
			AND ($4::text IS NULL OR be.utm_source = $4)
			AND ($5::text IS NULL OR be.device = $5)
			AND ($6::text IS NULL OR be.country = $6)
		GROUP BY be.post_slug, bp.title
		ORDER BY views DESC
		LIMIT 20`, f.From, f.To, f.Referrer, f.UTMSource, f.Device, f.Country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BlogMetricsTopPost{}
	for rows.Next() {
		var p BlogMetricsTopPost
		if err := rows.Scan(&p.PostSlug, &p.Title, &p.Views, &p.CTAClicks); err != nil {
			return nil, err
		}
		if p.Views > 0 {
			p.ConversionRate = float64(p.CTAClicks) / float64(p.Views)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// blogMetricsTopColumn agrega contagem de pageviews por uma coluna categórica
// (referrer, utm_source, device ou country) — as 4 dimensões do MVP compartilham
// a mesma forma de query, só troca o nome da coluna (nunca vem de input do
// usuário, sempre uma constante Go chamada internamente — sem risco de injeção).
func (s *Server) blogMetricsTopColumn(ctx context.Context, column string, f BlogMetricsFilter) ([]BlogMetricsCount, error) {
	sql := `
		SELECT ` + column + `, count(*)
		FROM blog_events
		WHERE created_at >= $1 AND created_at < $2 AND type = 'pageview'
			AND ` + column + ` IS NOT NULL AND ` + column + ` <> ''
			AND ($3::text IS NULL OR post_slug = $3)
			AND ($4::text IS NULL OR referrer = $4)
			AND ($5::text IS NULL OR utm_source = $5)
			AND ($6::text IS NULL OR device = $6)
			AND ($7::text IS NULL OR country = $7)
		GROUP BY ` + column + `
		ORDER BY count(*) DESC
		LIMIT 20`
	rows, err := s.db.Query(ctx, sql, f.From, f.To, f.PostSlug, f.Referrer, f.UTMSource, f.Device, f.Country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BlogMetricsCount{}
	for rows.Next() {
		var c BlogMetricsCount
		if err := rows.Scan(&c.Key, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Server) blogMetricsReferrers(ctx context.Context, f BlogMetricsFilter) ([]BlogMetricsCount, error) {
	return s.blogMetricsTopColumn(ctx, "referrer", f)
}

func (s *Server) blogMetricsUTMSource(ctx context.Context, f BlogMetricsFilter) ([]BlogMetricsCount, error) {
	return s.blogMetricsTopColumn(ctx, "utm_source", f)
}

func (s *Server) blogMetricsDevices(ctx context.Context, f BlogMetricsFilter) ([]BlogMetricsCount, error) {
	return s.blogMetricsTopColumn(ctx, "device", f)
}

func (s *Server) blogMetricsCountries(ctx context.Context, f BlogMetricsFilter) ([]BlogMetricsCount, error) {
	return s.blogMetricsTopColumn(ctx, "country", f)
}

// deleteOldBlogEvents apaga eventos com mais de 180 dias — mesma janela do
// analytics do loja-3d (referência: Marco Civil da Internet). Chamado
// periodicamente por uma goroutine em main.go (Task 6).
func (s *Server) deleteOldBlogEvents(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM blog_events WHERE created_at < now() - interval '180 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
