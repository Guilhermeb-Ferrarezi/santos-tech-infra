package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateIPBanBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleCreateIPBan(w, httptest.NewRequest("POST", "/auth/admin/ip-bans", strings.NewReader("xxx")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

func TestCreateIPBanInvalidIP(t *testing.T) {
	s := testServer(Config{})
	cases := []string{"", "not-an-ip", "999.999.999.999", "10.0.0.1/24"}
	for _, ip := range cases {
		w := httptest.NewRecorder()
		body := `{"ip":"` + ip + `"}`
		s.handleCreateIPBan(w, httptest.NewRequest("POST", "/auth/admin/ip-bans", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("ip=%q: code=%d, esperado 400", ip, w.Code)
		}
		if !strings.Contains(w.Body.String(), "INVALID_IP") {
			t.Fatalf("ip=%q: corpo=%q, esperava code INVALID_IP", ip, w.Body.String())
		}
	}
}

func TestCreateIPBanInvalidExpiresAt(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	body := `{"ip":"1.2.3.4","expires_at":"amanhã"}`
	s.handleCreateIPBan(w, httptest.NewRequest("POST", "/auth/admin/ip-bans", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVALID_EXPIRES_AT") {
		t.Fatalf("corpo=%q, esperava code INVALID_EXPIRES_AT", w.Body.String())
	}
}

func TestCreateIPBanExpiresAtNoPassado(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	body := `{"ip":"1.2.3.4","expires_at":"2000-01-01T00:00:00Z"}`
	s.handleCreateIPBan(w, httptest.NewRequest("POST", "/auth/admin/ip-bans", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVALID_EXPIRES_AT") {
		t.Fatalf("corpo=%q, esperava code INVALID_EXPIRES_AT", w.Body.String())
	}
}

func TestDeleteIPBanBadID(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/auth/admin/ip-bans/nao-e-um-id", nil)
	r.SetPathValue("id", "nao-e-um-id")
	s.handleDeleteIPBan(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}
