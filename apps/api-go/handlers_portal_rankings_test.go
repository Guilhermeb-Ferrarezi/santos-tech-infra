package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPortalRankingsRoutesAuthBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		path string
	}{
		{"ranking notas", s.staffGuard(s.handlePortalRankingNotas), "/portal/rankings/notas"},
		{"ranking pontos", s.staffGuard(s.handlePortalRankingPontos), "/portal/rankings/pontos"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			w := httptest.NewRecorder()
			tc.h(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d want 401", w.Code)
			}
		})
	}
}
