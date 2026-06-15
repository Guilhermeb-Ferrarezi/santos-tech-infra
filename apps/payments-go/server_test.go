package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAdmin_SemToken401(t *testing.T) {
	s := &Server{cfg: Config{JWTSecret: "x"}}
	h := s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("esperava 401, veio %d", rec.Code)
	}
}
