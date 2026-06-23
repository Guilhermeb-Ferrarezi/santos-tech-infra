package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreviewReturnsNextRuns(t *testing.T) {
	s := &Server{}
	body := `{"scheduleCron":"0 9 * * *","timezone":"America/Sao_Paulo","count":3}`
	req := httptest.NewRequest(http.MethodPost, "/cron/preview", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handlePreview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Next []string `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Next) != 3 {
		t.Fatalf("esperava 3 execuções, veio %d", len(out.Next))
	}
	for i := 1; i < len(out.Next); i++ {
		if out.Next[i] <= out.Next[i-1] {
			t.Fatalf("execuções não crescentes: %v", out.Next)
		}
	}
}

func TestPreviewRejectsBadCron(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/cron/preview",
		strings.NewReader(`{"scheduleCron":"lixo","timezone":"America/Sao_Paulo"}`))
	rec := httptest.NewRecorder()
	s.handlePreview(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400, veio %d", rec.Code)
	}
}
