# Blog — Métricas: Backend (coleta + agregação) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Adicionar coleta de eventos anônimos do blog (`blog_events`) e os endpoints
de ingestão (público) e agregação (admin) em `apps/api-go`, seguindo exatamente o
padrão já usado por `blog.go`/`handlers_blog.go`/`task.go` — sem sqlc, sem query
builder dinâmico, SQL parametrizado direto no store.

**Architecture:** Um domínio novo, self-contained, em dois arquivos flat
(`blog_analytics.go` = models + store, `handlers_blog_analytics.go` = HTTP), plugado
em `routes.go` e `main.go` do jeito que `blog.go`/`task.go` já estão. Sem tabela
nova de config, sem serviço novo — tudo dentro do `api-go` existente.

**Tech Stack:** Go 1.25, `net/http` stdlib, `pgx/v5` (SQL direto, sem sqlc — este
domínio segue o padrão de `blog.go`/`task.go`, não o de `auth.sql.go`/`users.sql.go`).

**Este é o Plano 1 de 3** desta feature (spec completa em
`docs/superpowers/specs/2026-08-06-blog-metricas-design.md`). Os outros dois:
- Plano 2 — beacon no `blog/web` (dispara `pageview`/`cta_click`), depende deste.
- Plano 3 — dashboard visual em `dashboard/web` (grid customizável), depende deste.
Cada um é um documento de plano separado, produzido depois deste ser aprovado.

---

## Task 1: Migração — tabela `blog_events`

**Files:**
- Modify: `apps/api-go/db.go:277-278` (dentro da `const migration`, logo antes do
  fechamento com backtick)

- [ ] **Step 1: Adicionar o `CREATE TABLE` à migração**

Em `apps/api-go/db.go`, a `const migration` termina assim (linhas 277-278):

```go
INSERT INTO glossary_terms (term, definicao) SELECT 'Lint', 'Uma checagem automática que avisa se o código tem algum erro de estilo ou descuido, antes de ir pro ar.' WHERE NOT EXISTS (SELECT 1 FROM glossary_terms WHERE lower(term)='lint');
`
```

Troque para:

```go
INSERT INTO glossary_terms (term, definicao) SELECT 'Lint', 'Uma checagem automática que avisa se o código tem algum erro de estilo ou descuido, antes de ir pro ar.' WHERE NOT EXISTS (SELECT 1 FROM glossary_terms WHERE lower(term)='lint');
CREATE TABLE IF NOT EXISTS blog_events (
  id          BIGSERIAL PRIMARY KEY,
  type        TEXT NOT NULL CHECK (type IN ('pageview','cta_click')),
  post_slug   TEXT,
  path        TEXT NOT NULL,
  session_id  TEXT NOT NULL,
  visitor_id  TEXT NOT NULL,
  referrer    TEXT,
  utm_source  TEXT,
  device      TEXT NOT NULL DEFAULT '',
  browser     TEXT,
  os          TEXT,
  country     TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_blog_events_post_created ON blog_events(post_slug, created_at);
CREATE INDEX IF NOT EXISTS idx_blog_events_type_created ON blog_events(type, created_at);
CREATE INDEX IF NOT EXISTS idx_blog_events_created ON blog_events(created_at);
`
```

(Mesmo estilo de `blog_posts`/`glossary_terms` — colunas alinhadas, `CREATE TABLE
IF NOT EXISTS` + `CREATE INDEX IF NOT EXISTS`, tudo idempotente.)

- [ ] **Step 2: Verificar que compila**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro (é só uma string Go, mas confirma que não quebrou a sintaxe do
arquivo).

- [ ] **Step 3: Verificar a migração localmente**

Run (raiz do repo):
```bash
docker compose -f infra/docker-compose.yml up -d postgres redis
cd apps/api-go && go run .
```
Em outro terminal:
```bash
docker compose -f infra/docker-compose.yml exec postgres psql -U postgres -d santos_tech -c "\d blog_events"
```
Expected: a tabela aparece com as 12 colunas e os 3 índices. Pare o `go run .`
(Ctrl+C) depois de confirmar.

- [ ] **Step 4: Commit**

```bash
git add apps/api-go/db.go
git commit -m "feat(api-go): tabela blog_events pra coleta de métricas do blog"
```

---

## Task 2: Parser de User-Agent (`blogua.go`)

Função pura, sem I/O — o candidato certo pra TDD de verdade. Nunca confiar no que
o cliente manda como device/browser/os: sempre derivar aqui, no servidor, a partir
do header `User-Agent` real da requisição (mesma razão anti-forja do resto do repo).

**Files:**
- Create: `apps/api-go/blogua.go`
- Test: `apps/api-go/blogua_test.go`

- [ ] **Step 1: Escrever o teste (vai falhar — `parseUserAgent` ainda não existe)**

`apps/api-go/blogua_test.go`:

```go
package main

import "testing"

func TestParseUserAgent(t *testing.T) {
	cases := []struct {
		name       string
		ua         string
		device     string
		browser    string
		os         string
	}{
		{
			name:    "iphone safari",
			ua:      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			device:  "mobile", browser: "Safari", os: "iOS",
		},
		{
			name:    "android chrome",
			ua:      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36",
			device:  "mobile", browser: "Chrome", os: "Android",
		},
		{
			name:    "windows chrome desktop",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
			device:  "desktop", browser: "Chrome", os: "Windows",
		},
		{
			name:    "windows edge desktop",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Edg/125.0.0.0",
			device:  "desktop", browser: "Edge", os: "Windows",
		},
		{
			name:    "mac firefox desktop",
			ua:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:126.0) Gecko/20100101 Firefox/126.0",
			device:  "desktop", browser: "Firefox", os: "macOS",
		},
		{
			name:    "ipad tablet",
			ua:      "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			device:  "tablet", browser: "Safari", os: "iOS",
		},
		{
			name:    "vazio",
			ua:      "",
			device:  "desktop", browser: "", os: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUserAgent(tc.ua)
			if got.Device != tc.device {
				t.Errorf("device: got %q, want %q", got.Device, tc.device)
			}
			if got.Browser != tc.browser {
				t.Errorf("browser: got %q, want %q", got.Browser, tc.browser)
			}
			if got.OS != tc.os {
				t.Errorf("os: got %q, want %q", got.OS, tc.os)
			}
		})
	}
}
```

- [ ] **Step 2: Rodar o teste e confirmar que falha**

Run: `cd apps/api-go && go test ./... -run TestParseUserAgent -v`
Expected: FAIL — `undefined: parseUserAgent` (ou `UserAgentInfo` indefinido).

- [ ] **Step 3: Implementar `blogua.go`**

```go
package main

import "strings"

// UserAgentInfo é o resultado da heurística de parsing do User-Agent — nunca
// confiar no que o cliente declara, sempre derivar isto no servidor a partir do
// header real da requisição (mesmo motivo anti-forja do resto do domínio de
// analytics: o cliente não pode inflar/forjar a métrica).
type UserAgentInfo struct {
	Device  string // "mobile" | "tablet" | "desktop"
	Browser string // "Chrome" | "Safari" | "Firefox" | "Edge" | ""
	OS      string // "iOS" | "Android" | "Windows" | "macOS" | "Linux" | ""
}

// parseUserAgent é uma heurística simples baseada em substring — não é uma lib
// completa de UA sniffing, é o suficiente pra dashboard de métricas (mesmo nível
// de esforço que o loja-3d usa no próprio analytics).
func parseUserAgent(ua string) UserAgentInfo {
	var info UserAgentInfo

	switch {
	case strings.Contains(ua, "iPad"):
		info.Device = "tablet"
	case strings.Contains(ua, "Tablet") || strings.Contains(ua, "SM-T"):
		info.Device = "tablet"
	case strings.Contains(ua, "Mobi") || strings.Contains(ua, "iPhone") || strings.Contains(ua, "Android"):
		info.Device = "mobile"
	default:
		info.Device = "desktop"
	}
	// "Android" sozinho (sem "Mobi") em geral ainda é celular no nosso público;
	// só reclassifica como tablet se vier explicitamente marcado acima.
	if info.Device == "desktop" && strings.Contains(ua, "Android") {
		info.Device = "mobile"
	}

	switch {
	case strings.Contains(ua, "Edg/"):
		info.Browser = "Edge"
	case strings.Contains(ua, "Chrome/"):
		info.Browser = "Chrome"
	case strings.Contains(ua, "Firefox/"):
		info.Browser = "Firefox"
	case strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/"):
		info.Browser = "Safari"
	}

	switch {
	case strings.Contains(ua, "iPhone OS") || strings.Contains(ua, "CPU OS"):
		info.OS = "iOS"
	case strings.Contains(ua, "Android"):
		info.OS = "Android"
	case strings.Contains(ua, "Windows"):
		info.OS = "Windows"
	case strings.Contains(ua, "Mac OS X"):
		info.OS = "macOS"
	case strings.Contains(ua, "Linux"):
		info.OS = "Linux"
	}

	return info
}

// referrerDomain reduz uma URL de referrer completa ao domínio (ex.:
// "https://www.google.com/search?q=x" → "google.com") — evita que a mesma
// origem apareça picada em dezenas de linhas diferentes no ranking de
// referrers por causa de query string/path. "www." é removido para não
// duplicar linha com/sem www. String vazia ou não-parseável vira "" (direto).
func referrerDomain(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	s := raw
	if i := strings.Index(s, "://"); i != -1 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i != -1 {
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i != -1 { // user:pass@host raro, mas não deixa vazar
		s = s[i+1:]
	}
	s = strings.TrimPrefix(s, "www.")
	return strings.ToLower(s)
}
```

Nota: a heurística de `Device` tem uma reclassificação (`Android` sem `Mobi` vira
`mobile`) porque alguns UAs de Android não incluem o token `Mobi`. Isso é
intencional e coberto pelo caso de teste `"android chrome"`.

- [ ] **Step 4: Rodar o teste e confirmar que passa**

Run: `cd apps/api-go && go test ./... -run TestParseUserAgent -v`
Expected: PASS em todos os subtestes.

- [ ] **Step 5: Escrever teste de `referrerDomain`**

Adicionar em `apps/api-go/blogua_test.go`:

```go
func TestReferrerDomain(t *testing.T) {
	cases := map[string]string{
		"https://www.google.com/search?q=tela": "google.com",
		"https://instagram.com/":                "instagram.com",
		"http://t.co/abc123":                    "t.co",
		"":                                       "",
		"direto":                                 "direto",
	}
	for in, want := range cases {
		if got := referrerDomain(in); got != want {
			t.Errorf("referrerDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 6: Rodar e confirmar que passa**

Run: `cd apps/api-go && go test ./... -run TestReferrerDomain -v`
Expected: PASS.

- [ ] **Step 7: `gofmt` + `go vet`**

Run: `cd apps/api-go && gofmt -l . && go vet ./...`
Expected: `gofmt -l .` sem saída nenhuma; `go vet` sem erro.

- [ ] **Step 8: Commit**

```bash
git add apps/api-go/blogua.go apps/api-go/blogua_test.go
git commit -m "feat(api-go): parser de User-Agent e domínio de referrer pro blog analytics"
```

---

## Task 3: Domínio — models e store (`blog_analytics.go`)

**Files:**
- Create: `apps/api-go/blog_analytics.go`

- [ ] **Step 1: Escrever models + input da ingestão**

```go
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
	Views          int64   `json:"views"`
	CTAClicks      int64   `json:"ctaClicks"`
	ConversionRate float64 `json:"conversionRate"`
}

type BlogMetricsCount struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// BlogMetricsFilter é comum a todos os endpoints de agregação: intervalo de
// tempo obrigatório, post opcional (nil = todos).
type BlogMetricsFilter struct {
	From     time.Time
	To       time.Time
	PostSlug *string
}
```

- [ ] **Step 2: Store — inserção do evento**

Adicionar ao mesmo arquivo `apps/api-go/blog_analytics.go`:

```go
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
```

- [ ] **Step 3: Store — overview (com comparação vs período anterior)**

```go
// ── Store — agregação ────────────────────────────────────────────────────────

const blogOverviewSQL = `
SELECT
	count(*) FILTER (WHERE type='pageview') AS pageviews,
	count(DISTINCT visitor_id) AS visitors,
	count(*) FILTER (WHERE type='cta_click') AS cta_clicks
FROM blog_events
WHERE created_at >= $1 AND created_at < $2
	AND ($3::text IS NULL OR post_slug = $3)`

func (s *Server) blogMetricsOverview(ctx context.Context, f BlogMetricsFilter) (*BlogMetricsOverview, error) {
	var out BlogMetricsOverview
	if err := s.db.QueryRow(ctx, blogOverviewSQL, f.From, f.To, f.PostSlug).
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
	if err := s.db.QueryRow(ctx, blogOverviewSQL, prevFrom, prevTo, f.PostSlug).
		Scan(&out.PrevPageviews, &out.PrevVisitors, &out.PrevCTAClicks); err != nil {
		return nil, err
	}
	if out.PrevVisitors > 0 {
		out.PrevConversionRate = float64(out.PrevCTAClicks) / float64(out.PrevVisitors)
	}
	return &out, nil
}
```

- [ ] **Step 4: Store — série temporal (diária ou horária)**

```go
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
		GROUP BY gs.bucket
		ORDER BY gs.bucket`
	rows, err := s.db.Query(ctx, sql, f.From, f.To, f.PostSlug, unit)
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
```

- [ ] **Step 5: Store — top posts**

```go
func (s *Server) blogMetricsTopPosts(ctx context.Context, f BlogMetricsFilter) ([]BlogMetricsTopPost, error) {
	rows, err := s.db.Query(ctx, `
		SELECT post_slug,
			count(*) FILTER (WHERE type='pageview') AS views,
			count(*) FILTER (WHERE type='cta_click') AS cta_clicks
		FROM blog_events
		WHERE created_at >= $1 AND created_at < $2 AND post_slug IS NOT NULL
		GROUP BY post_slug
		ORDER BY views DESC
		LIMIT 20`, f.From, f.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BlogMetricsTopPost{}
	for rows.Next() {
		var p BlogMetricsTopPost
		if err := rows.Scan(&p.PostSlug, &p.Views, &p.CTAClicks); err != nil {
			return nil, err
		}
		if p.Views > 0 {
			p.ConversionRate = float64(p.CTAClicks) / float64(p.Views)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
```

- [ ] **Step 6: Store — referrers, UTM source, dispositivo, país**

```go
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
		GROUP BY ` + column + `
		ORDER BY count(*) DESC
		LIMIT 20`
	rows, err := s.db.Query(ctx, sql, f.From, f.To, f.PostSlug)
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
```

- [ ] **Step 7: Store — retenção (purge de 180 dias)**

```go
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
```

- [ ] **Step 8: `gofmt` + `go vet` + `go build`**

Run: `cd apps/api-go && gofmt -l . && go vet ./... && go build ./...`
Expected: tudo limpo (sem saída do gofmt, sem erro).

- [ ] **Step 9: Commit**

```bash
git add apps/api-go/blog_analytics.go
git commit -m "feat(api-go): store de coleta e agregação do blog analytics"
```

---

## Task 4: Handlers HTTP (`handlers_blog_analytics.go`)

**Files:**
- Create: `apps/api-go/handlers_blog_analytics.go`
- Test: `apps/api-go/handlers_blog_analytics_test.go`

- [ ] **Step 1: Escrever os testes de validação (vão falhar — handlers não existem)**

`apps/api-go/handlers_blog_analytics_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleBlogEventIngestValidation(t *testing.T) {
	s := testServer(Config{})

	cases := []struct {
		name string
		body string
	}{
		{"corpo inválido", "xxx"},
		{"type vazio", `{"type":"","path":"/blog","sessionId":"s1","visitorId":"v1"}`},
		{"type desconhecido", `{"type":"click","path":"/blog","sessionId":"s1","visitorId":"v1"}`},
		{"path vazio", `{"type":"pageview","path":"","sessionId":"s1","visitorId":"v1"}`},
		{"sessionId vazio", `{"type":"pageview","path":"/blog","sessionId":"","visitorId":"v1"}`},
		{"visitorId vazio", `{"type":"pageview","path":"/blog","sessionId":"s1","visitorId":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/public/blog/events", strings.NewReader(tc.body))
			s.handleBlogEventIngest(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// handleBlogMetricsOverview em si não checa auth — o guard fica em routes.go
// (Task 5), por isso o teste envolve o handler com s.permGuard explicitamente,
// mesmo padrão de TestPermGuardTasksNoToken em handlers_tasks_test.go.
func TestPermGuardBlogMetricsNoToken(t *testing.T) {
	s := testServer(Config{})
	guarded := s.permGuard("blog_posts", "read", false, s.handleBlogMetricsOverview)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin/blog/metrics/overview", nil)
	guarded(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("sem token: code=%d, want 401", w.Code)
	}
}

func TestBlogMetricsParamsMissingFrom(t *testing.T) {
	r := httptest.NewRequest("GET", "/admin/blog/metrics/overview?to=2026-08-06", nil)
	_, err := blogMetricsParamsFrom(r)
	if err == nil {
		t.Fatal("esperava erro com 'from' ausente")
	}
}

func TestBlogMetricsParamsInvalidDate(t *testing.T) {
	r := httptest.NewRequest("GET", "/admin/blog/metrics/overview?from=not-a-date&to=2026-08-06", nil)
	_, err := blogMetricsParamsFrom(r)
	if err == nil {
		t.Fatal("esperava erro com 'from' inválido")
	}
}

func TestBlogMetricsParamsOK(t *testing.T) {
	r := httptest.NewRequest("GET", "/admin/blog/metrics/overview?from=2026-08-01&to=2026-08-06&postSlug=meu-post", nil)
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if f.PostSlug == nil || *f.PostSlug != "meu-post" {
		t.Fatalf("postSlug: got %v", f.PostSlug)
	}
	if !f.To.After(f.From) {
		t.Fatalf("to deveria ser depois de from")
	}
}
```

- [ ] **Step 2: Rodar os testes e confirmar que falham**

Run: `cd apps/api-go && go test ./... -run "TestHandleBlogEventIngestValidation|TestPermGuardBlogMetricsNoToken|TestBlogMetricsParams" -v`
Expected: FAIL — `undefined: handleBlogEventIngest` (e os demais símbolos ainda
não existem).

- [ ] **Step 3: Implementar `handlers_blog_analytics.go`**

```go
package main

import (
	"net/http"
	"strings"
	"time"
)

// ── Ingestão (público, sem auth) ────────────────────────────────────────────

func validateBlogEventInput(in *BlogEventInput) error {
	in.Type = strings.TrimSpace(in.Type)
	in.Path = strings.TrimSpace(in.Path)
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.VisitorID = strings.TrimSpace(in.VisitorID)
	if !validBlogEventTypes[in.Type] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "type inválido")
	}
	if in.Path == "" || len(in.Path) > 512 {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "path inválido")
	}
	if in.SessionID == "" || len(in.SessionID) > 128 {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "sessionId inválido")
	}
	if in.VisitorID == "" || len(in.VisitorID) > 128 {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "visitorId inválido")
	}
	if in.PostSlug != nil {
		slug := strings.TrimSpace(*in.PostSlug)
		if slug == "" {
			in.PostSlug = nil
		} else {
			in.PostSlug = &slug
		}
	}
	return nil
}

// POST /public/blog/events
func (s *Server) handleBlogEventIngest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10) // 4KB — beacon é pequeno
	var in BlogEventInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateBlogEventInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	country := r.Header.Get("CF-IPCountry")
	if err := s.insertBlogEvent(r.Context(), in, r.UserAgent(), country); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Agregação (admin — blog_posts:read) ─────────────────────────────────────

// blogMetricsParamsFrom lê from/to (YYYY-MM-DD) e postSlug opcional da
// querystring. "to" é exclusivo e vira o FIM do dia informado (23:59:59.999...)
// pra incluir o dia inteiro.
func blogMetricsParamsFrom(r *http.Request) (BlogMetricsFilter, error) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if fromStr == "" || toStr == "" {
		return BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "from e to são obrigatórios")
	}
	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "from inválido (use YYYY-MM-DD)")
	}
	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "to inválido (use YYYY-MM-DD)")
	}
	to = to.Add(24 * time.Hour) // exclusivo: inclui o dia "to" inteiro
	if !to.After(from) {
		return BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "to deve ser depois de from")
	}
	f := BlogMetricsFilter{From: from, To: to}
	if slug := strings.TrimSpace(r.URL.Query().Get("postSlug")); slug != "" {
		f.PostSlug = &slug
	}
	return f, nil
}

// GET /admin/blog/metrics/overview
func (s *Server) handleBlogMetricsOverview(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsOverview(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /admin/blog/metrics/timeseries
func (s *Server) handleBlogMetricsTimeseries(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsTimeseries(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /admin/blog/metrics/top-posts
func (s *Server) handleBlogMetricsTopPosts(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsTopPosts(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /admin/blog/metrics/referrers
func (s *Server) handleBlogMetricsReferrers(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsReferrers(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /admin/blog/metrics/utm-source
func (s *Server) handleBlogMetricsUTMSource(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsUTMSource(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /admin/blog/metrics/devices
func (s *Server) handleBlogMetricsDevices(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsDevices(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /admin/blog/metrics/countries
func (s *Server) handleBlogMetricsCountries(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsCountries(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
```

- [ ] **Step 4: Rodar os testes e confirmar que passam**

Run: `cd apps/api-go && go test ./... -run "TestHandleBlogEventIngestValidation|TestPermGuardBlogMetricsNoToken|TestBlogMetricsParams" -v`
Expected: PASS em todos.

- [ ] **Step 5: `gofmt` + `go vet` + `go build`**

Run: `cd apps/api-go && gofmt -l . && go vet ./... && go build ./...`
Expected: tudo limpo.

- [ ] **Step 6: Commit**

```bash
git add apps/api-go/handlers_blog_analytics.go apps/api-go/handlers_blog_analytics_test.go
git commit -m "feat(api-go): handlers de ingestão e agregação do blog analytics"
```

---

## Task 5: Registrar as rotas

**Files:**
- Modify: `apps/api-go/routes.go:184` (logo depois das rotas públicas de blog
  existentes)

- [ ] **Step 1: Adicionar as rotas**

Em `apps/api-go/routes.go`, logo depois da linha:

```go
	mux.HandleFunc("GET /public/blog/categories", s.rateLimit(120, min, s.handleListBlogCategories))
```

adicionar:

```go
	// Beacon de analytics do blog — público, sem auth, rate limit mais folgado
	// que leitura normal (é chamado a cada pageview + cada clique de CTA).
	mux.HandleFunc("POST /public/blog/events", s.rateLimit(300, min, s.handleBlogEventIngest))
```

E logo depois do bloco de `/blog/categories` admin (linha 197,
`DELETE /blog/categories/{id}`), adicionar:

```go
	// Métricas do blog — admin, mesma permissão de quem já lê posts (blog_posts:read).
	mux.HandleFunc("GET /admin/blog/metrics/overview", s.permGuard("blog_posts", "read", false, s.handleBlogMetricsOverview))
	mux.HandleFunc("GET /admin/blog/metrics/timeseries", s.permGuard("blog_posts", "read", false, s.handleBlogMetricsTimeseries))
	mux.HandleFunc("GET /admin/blog/metrics/top-posts", s.permGuard("blog_posts", "read", false, s.handleBlogMetricsTopPosts))
	mux.HandleFunc("GET /admin/blog/metrics/referrers", s.permGuard("blog_posts", "read", false, s.handleBlogMetricsReferrers))
	mux.HandleFunc("GET /admin/blog/metrics/utm-source", s.permGuard("blog_posts", "read", false, s.handleBlogMetricsUTMSource))
	mux.HandleFunc("GET /admin/blog/metrics/devices", s.permGuard("blog_posts", "read", false, s.handleBlogMetricsDevices))
	mux.HandleFunc("GET /admin/blog/metrics/countries", s.permGuard("blog_posts", "read", false, s.handleBlogMetricsCountries))
```

- [ ] **Step 2: `go build`**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro.

- [ ] **Step 3: Teste manual local**

Com o servidor local rodando (`go run .`, Postgres/Redis up):

```bash
curl -X POST http://localhost:3333/public/blog/events \
  -H "Content-Type: application/json" \
  -d '{"type":"pageview","path":"/blog/post/tempo-de-tela","postSlug":"tempo-de-tela","sessionId":"s1","visitorId":"v1","referrer":"https://google.com/search","utmSource":"instagram"}'
```
Expected: `204 No Content`.

```bash
docker compose -f infra/docker-compose.yml exec postgres psql -U postgres -d santos_tech -c "SELECT type, post_slug, device, referrer, utm_source FROM blog_events;"
```
Expected: 1 linha, `device` preenchido (o `curl` manda um UA genérico ou vazio —
tudo bem, o parser trata isso), `referrer = 'google.com'`, `utm_source = 'instagram'`.

- [ ] **Step 4: Commit**

```bash
git add apps/api-go/routes.go
git commit -m "feat(api-go): registra rotas de ingestão e agregação do blog analytics"
```

---

## Task 6: Retenção — goroutine de purge (180 dias)

**Files:**
- Modify: `apps/api-go/main.go:88-110` (logo depois do bloco de cleanup de
  sessões existente)

- [ ] **Step 1: Adicionar a goroutine**

Em `apps/api-go/main.go`, logo depois do bloco (linhas 89-110):

```go
	// Limpeza periódica de sessões vencidas (sem isso a tabela cresce pra sempre).
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic no cleanup de sessões", "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if n, err := srv.deleteExpiredSessions(ctx); err != nil {
				slog.Warn("cleanup de sessões falhou", "err", err)
			} else if n > 0 {
				slog.Info("sessões vencidas removidas", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
```

adicionar (mesmo padrão, intervalo diário em vez de horário — retenção de 180
dias não precisa checar toda hora):

```go
	// Retenção de eventos de analytics do blog — apaga o que passou de 180 dias
	// (mesma janela do analytics do loja-3d, referência Marco Civil da Internet).
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic no purge de blog_events", "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			if n, err := srv.deleteOldBlogEvents(ctx); err != nil {
				slog.Warn("purge de blog_events falhou", "err", err)
			} else if n > 0 {
				slog.Info("eventos de blog analytics expirados removidos", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
```

- [ ] **Step 2: `go build`**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro.

- [ ] **Step 3: Commit**

```bash
git add apps/api-go/main.go
git commit -m "feat(api-go): retenção de 180 dias pros eventos de blog analytics"
```

---

## Task 7: Documentação — `openapi.yaml` e `llms.txt`

Regra do próprio repo (`CLAUDE.md`): rota nova → `docs/openapi.yaml` no mesmo
commit; contrato novo → `apps/api-go/llms.txt` também.

**Files:**
- Modify: `docs/openapi.yaml`
- Modify: `apps/api-go/llms.txt`

- [ ] **Step 1: Adicionar ao `docs/openapi.yaml`**

Abra `docs/openapi.yaml`, ache o bloco `paths:` e adicione os 7 endpoints — os 6
de agregação compartilham a mesma forma (`from`/`to`/`postSlug` na query, mesma
`security`, mesmos códigos de erro), só muda `summary` e o path:

```yaml
  /public/blog/events:
    post:
      summary: Registra um evento de analytics do blog (beacon anônimo)
      tags: [Blog Analytics]
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [type, path, sessionId, visitorId]
              properties:
                type: { type: string, enum: [pageview, cta_click] }
                path: { type: string }
                postSlug: { type: string, nullable: true }
                sessionId: { type: string }
                visitorId: { type: string }
                referrer: { type: string }
                utmSource: { type: string }
      responses:
        "204": { description: Evento registrado }
        "400": { description: Corpo inválido }
        "429": { description: Rate limit excedido }
  /admin/blog/metrics/overview:
    get:
      summary: Resumo de pageviews/visitantes/cliques CTA com comparação vs período anterior
      tags: [Blog Analytics]
      security: [{ cookieAuth: [] }, { bearerAuth: [] }]
      parameters:
        - { name: from, in: query, required: true, schema: { type: string, format: date } }
        - { name: to, in: query, required: true, schema: { type: string, format: date } }
        - { name: postSlug, in: query, required: false, schema: { type: string } }
      responses:
        "200": { description: OK }
        "400": { description: from/to ausentes ou inválidos }
        "401": { description: Não autenticado }
        "403": { description: Sem permissão blog_posts:read }
  /admin/blog/metrics/timeseries:
    get:
      summary: Série temporal de pageviews (diária, ou horária se from/to ≤ 24h)
      tags: [Blog Analytics]
      security: [{ cookieAuth: [] }, { bearerAuth: [] }]
      parameters:
        - { name: from, in: query, required: true, schema: { type: string, format: date } }
        - { name: to, in: query, required: true, schema: { type: string, format: date } }
        - { name: postSlug, in: query, required: false, schema: { type: string } }
      responses:
        "200": { description: OK }
        "400": { description: from/to ausentes ou inválidos }
        "401": { description: Não autenticado }
        "403": { description: Sem permissão blog_posts:read }
  /admin/blog/metrics/top-posts:
    get:
      summary: Ranking de posts por pageviews, com cliques de CTA e taxa de conversão
      tags: [Blog Analytics]
      security: [{ cookieAuth: [] }, { bearerAuth: [] }]
      parameters:
        - { name: from, in: query, required: true, schema: { type: string, format: date } }
        - { name: to, in: query, required: true, schema: { type: string, format: date } }
      responses:
        "200": { description: OK }
        "400": { description: from/to ausentes ou inválidos }
        "401": { description: Não autenticado }
        "403": { description: Sem permissão blog_posts:read }
  /admin/blog/metrics/referrers:
    get:
      summary: Top domínios de referrer (dedup por domínio, sem query string)
      tags: [Blog Analytics]
      security: [{ cookieAuth: [] }, { bearerAuth: [] }]
      parameters:
        - { name: from, in: query, required: true, schema: { type: string, format: date } }
        - { name: to, in: query, required: true, schema: { type: string, format: date } }
        - { name: postSlug, in: query, required: false, schema: { type: string } }
      responses:
        "200": { description: OK }
        "400": { description: from/to ausentes ou inválidos }
        "401": { description: Não autenticado }
        "403": { description: Sem permissão blog_posts:read }
  /admin/blog/metrics/utm-source:
    get:
      summary: Top utm_source de entrada
      tags: [Blog Analytics]
      security: [{ cookieAuth: [] }, { bearerAuth: [] }]
      parameters:
        - { name: from, in: query, required: true, schema: { type: string, format: date } }
        - { name: to, in: query, required: true, schema: { type: string, format: date } }
        - { name: postSlug, in: query, required: false, schema: { type: string } }
      responses:
        "200": { description: OK }
        "400": { description: from/to ausentes ou inválidos }
        "401": { description: Não autenticado }
        "403": { description: Sem permissão blog_posts:read }
  /admin/blog/metrics/devices:
    get:
      summary: Breakdown de pageviews por dispositivo (mobile/tablet/desktop)
      tags: [Blog Analytics]
      security: [{ cookieAuth: [] }, { bearerAuth: [] }]
      parameters:
        - { name: from, in: query, required: true, schema: { type: string, format: date } }
        - { name: to, in: query, required: true, schema: { type: string, format: date } }
        - { name: postSlug, in: query, required: false, schema: { type: string } }
      responses:
        "200": { description: OK }
        "400": { description: from/to ausentes ou inválidos }
        "401": { description: Não autenticado }
        "403": { description: Sem permissão blog_posts:read }
  /admin/blog/metrics/countries:
    get:
      summary: Breakdown de pageviews por país (via header CF-IPCountry)
      tags: [Blog Analytics]
      security: [{ cookieAuth: [] }, { bearerAuth: [] }]
      parameters:
        - { name: from, in: query, required: true, schema: { type: string, format: date } }
        - { name: to, in: query, required: true, schema: { type: string, format: date } }
        - { name: postSlug, in: query, required: false, schema: { type: string } }
      responses:
        "200": { description: OK }
        "400": { description: from/to ausentes ou inválidos }
        "401": { description: Não autenticado }
        "403": { description: Sem permissão blog_posts:read }
```

- [ ] **Step 2: Adicionar ao `apps/api-go/llms.txt`**

Ache a seção `## Glossário` (perto do fim das seções de domínio) e adicione,
logo antes dela, uma seção nova no mesmo estilo (nota: **não existe hoje** uma
seção `## Blog` documentando `/blog/posts`/`/public/blog/*` — gap pré-existente,
fora do escopo deste plano; só documente aqui os endpoints de analytics):

```
## Blog Analytics — `api.santos-tech.com/blog/events` e `/admin/blog/metrics`

Coleta de eventos anônimos do blog público (pageviews e cliques no CTA) e
agregação pra dashboard admin. Beacon sem auth; agregação exige `blog_posts:read`.

- `POST /public/blog/events {type,path,postSlug?,sessionId,visitorId,referrer?,utmSource?}`
  → 204 · rate limit 300/min/IP · `device`/`browser`/`os`/`country` são sempre
  derivados no servidor a partir de User-Agent/CF-IPCountry, nunca aceitos do payload.
- `GET /admin/blog/metrics/overview?from&to&postSlug?` — `{pageviews,visitors,
  ctaClicks,conversionRate,prev*}` (comparação vs período anterior de mesma duração)
- `GET /admin/blog/metrics/timeseries?from&to&postSlug?` — `[{bucket,pageviews}]`,
  diário (ou horário se `to-from ≤ 24h`)
- `GET /admin/blog/metrics/top-posts?from&to` — `[{postSlug,views,ctaClicks,conversionRate}]`
- `GET /admin/blog/metrics/referrers?from&to&postSlug?` — `[{key,count}]` por domínio
- `GET /admin/blog/metrics/utm-source?from&to&postSlug?` — `[{key,count}]`
- `GET /admin/blog/metrics/devices?from&to&postSlug?` — `[{key,count}]` (mobile/tablet/desktop)
- `GET /admin/blog/metrics/countries?from&to&postSlug?` — `[{key,count}]`

Retenção: eventos com mais de 180 dias são apagados automaticamente (goroutine
diária em `main.go`).
```

- [ ] **Step 3: Commit**

```bash
git add docs/openapi.yaml apps/api-go/llms.txt
git commit -m "docs(api-go): documenta endpoints de blog analytics no openapi e llms.txt"
```

---

## Task 8: Verificação final completa

- [ ] **Step 1: Suite completa**

Run:
```bash
cd apps/api-go
gofmt -l .
go vet ./...
go build ./...
go test ./...
```
Expected: `gofmt -l .` sem saída; `go vet`/`go build` sem erro; `go test ./...`
todos PASS (os testes de integração que dependem de Postgres/Redis reais só
rodam se `docker compose ... up -d postgres redis` estiver de pé — suba antes se
algum teste pré-existente pedir).

- [ ] **Step 2: Push**

```bash
git push
```

Isso conclui o Plano 1. Os Planos 2 (beacon no `blog/web`) e 3 (dashboard em
`dashboard/web`) dependem dos endpoints deste plano estarem no ar.
