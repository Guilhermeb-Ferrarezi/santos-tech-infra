package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func patchUserReq(body string) *http.Request {
	r := httptest.NewRequest("PATCH", "/auth/admin/users/55", strings.NewReader(body))
	r.SetPathValue("id", "55")
	return reqAs(r, 1)
}

func TestPatchUserPermissoesInvalidas400(t *testing.T) {
	s := testServer(Config{JWTSecret: "segredo-teste"})
	w := httptest.NewRecorder()
	s.handleUpdateAdminUser(w, patchUserReq(`{"permissions":{"Dispositivos":["ver"]}}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
}

func TestPatchUserPermissoesSemSudo403(t *testing.T) {
	s := testServer(Config{JWTSecret: "segredo-teste"})
	w := httptest.NewRecorder()
	s.handleUpdateAdminUser(w, patchUserReq(`{"permissions":{"dispositivos":["ver"]}}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
	var e struct{ Code string }
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	if e.Code != "SUDO_REQUIRED" {
		t.Fatalf("code de erro=%q", e.Code)
	}
}

func TestPatchUserRole4SemCargoNaoE400(t *testing.T) {
	// Sem banco (s.db nil) a atualização em si falha depois — o que importa é
	// NÃO barrar na validação ("customRoleId obrigatório" deixou de existir).
	s := testServer(Config{JWTSecret: "segredo-teste"})
	w := httptest.NewRecorder()
	func() {
		defer func() { _ = recover() }() // s.db nil pode entrar em pânico ao chegar no banco
		s.handleUpdateAdminUser(w, patchUserReq(`{"role":4}`))
	}()
	if w.Code == http.StatusBadRequest {
		t.Fatalf("role=4 sem customRoleId não pode mais ser 400: %s", w.Body)
	}
}
