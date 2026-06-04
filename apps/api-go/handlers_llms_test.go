package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLLMsTxtServeConteudo(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleLLMsTxt(w, httptest.NewRequest("GET", "/llms.txt", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type=%q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"# Santos Tech", "/auth/login", "/claude/", "mails.santos-tech.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("llms.txt sem %q", want)
		}
	}
}
