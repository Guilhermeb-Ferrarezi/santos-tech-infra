package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListCustomRolesNoToken(t *testing.T) {
	s := testServer(Config{})
	h := s.adminGuard(s.handleListCustomRoles)
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/auth/admin/custom-roles", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, esperado 401", w.Code)
	}
}

func TestCreateCustomRoleBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleCreateCustomRole(w, httptest.NewRequest("POST", "/auth/admin/custom-roles", strings.NewReader("xxx")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

func TestCreateCustomRoleMissingName(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleCreateCustomRole(w, httptest.NewRequest("POST", "/auth/admin/custom-roles",
		strings.NewReader(`{"permissions":{}}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

func TestGetCustomRoleBadID(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/auth/admin/custom-roles/nao-uuid", nil)
	r.SetPathValue("id", "nao-uuid")
	s.handleGetCustomRole(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

func TestUpdateCustomRoleBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PATCH", "/auth/admin/custom-roles/00000000-0000-0000-0000-000000000001",
		strings.NewReader("xxx"))
	r.SetPathValue("id", "00000000-0000-0000-0000-000000000001")
	s.handleUpdateCustomRole(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

func TestDeleteCustomRoleBadID(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/auth/admin/custom-roles/nao-uuid", nil)
	r.SetPathValue("id", "nao-uuid")
	s.handleDeleteCustomRole(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}
