package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShortCommit(t *testing.T) {
	cases := map[string]string{
		"8921f2331c9cd17eebcc38f4e5c2af196112e1cb": "8921f23",
		"8921F23":                   "8921f23",
		"abc":                       "unknown", // curto demais pra ser sha
		"":                          "unknown",
		"8921f23; rm -rf":           "unknown", // só hex passa — nada além do sha vaza
		"<script>alert(1)</script>": "unknown",
	}
	for in, want := range cases {
		if got := shortCommit(in); got != want {
			t.Errorf("shortCommit(%q) = %q, quer %q", in, got, want)
		}
	}
}

func TestHealthDevolveVersao(t *testing.T) {
	t.Setenv("SOURCE_COMMIT", "8921f2331c9cd17eebcc38f4e5c2af196112e1cb")
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleHealth(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("JSON inválido: %v (%s)", err, w.Body.String())
	}
	if body["ok"] != true || body["version"] != "8921f23" {
		t.Fatalf("body = %v, quer ok=true version=8921f23", body)
	}
	if len(body) != 2 {
		t.Fatalf("health não deveria expor nada além de ok/version: %v", body)
	}
}
