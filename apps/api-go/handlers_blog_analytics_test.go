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

func TestBlogMetricsParamsMaxRange(t *testing.T) {
	// Intervalo de 2 anos: deve ser rejeitado (limite é 366 dias).
	r := httptest.NewRequest("GET", "/blog/metrics/overview?from=2024-01-01&to=2026-01-01", nil)
	_, err := blogMetricsParamsFrom(r)
	if err == nil {
		t.Fatal("esperava erro para intervalo maior que 366 dias")
	}
}

func TestBlogMetricsParamsMaxRangeExact(t *testing.T) {
	// 365 dias de intervalo: deve ser aceito.
	r := httptest.NewRequest("GET", "/blog/metrics/overview?from=2025-08-08&to=2026-08-08", nil)
	_, err := blogMetricsParamsFrom(r)
	if err != nil {
		t.Fatalf("365 dias deve ser aceito: %v", err)
	}
}
