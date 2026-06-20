package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// adminGuard sem cookie/Authorization retorna 401 ANTES de tocar no banco.
func TestAdminGuardNoToken(t *testing.T) {
	s := testServer(Config{})
	h := s.adminGuard(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/auth/admin/users", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, esperado 401", w.Code)
	}
}

// handleCreateAdminUser com corpo inválido → 400 antes do banco.
func TestCreateAdminUserBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleCreateAdminUser(w, httptest.NewRequest("POST", "/auth/admin/users", strings.NewReader("xxx")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// localPart inválido (vazio / com @ / com espaço) → 400 antes do banco.
func TestCreateAdminUserBadLocalPart(t *testing.T) {
	s := testServer(Config{})
	for _, body := range []string{
		`{"localPart":"","name":"X"}`,
		`{"localPart":"joao@x","name":"X"}`,
		`{"localPart":"jo ao","name":"X"}`,
	} {
		w := httptest.NewRecorder()
		s.handleCreateAdminUser(w, httptest.NewRequest("POST", "/auth/admin/users", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%s code=%d, esperado 400", body, w.Code)
		}
	}
}

// email longo (> 254 chars, RFC 5321) → 400 antes do banco.
func TestCreateAdminUserEmailTooLong(t *testing.T) {
	s := testServer(Config{})
	longEmail := strings.Repeat("a", 249) + "@b.com" // 255 chars > 254
	w := httptest.NewRecorder()
	s.handleCreateAdminUser(w, httptest.NewRequest("POST", "/auth/admin/users",
		strings.NewReader(`{"email":"`+longEmail+`","name":"X"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("email longo: code=%d, esperado 400", w.Code)
	}
}

// role fora de {1,2,3} → 400 antes do banco.
func TestCreateAdminUserBadRole(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleCreateAdminUser(w, httptest.NewRequest("POST", "/auth/admin/users",
		strings.NewReader(`{"localPart":"joao","name":"João","role":9}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// id não-numérico → 400 antes do banco.
func TestUpdateAdminUserBadID(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PATCH", "/auth/admin/users/abc", strings.NewReader(`{}`))
	r.SetPathValue("id", "abc")
	s.handleUpdateAdminUser(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

func TestDeleteAdminUserBadID(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/auth/admin/users/abc", nil)
	r.SetPathValue("id", "abc")
	s.handleDeleteAdminUser(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// role=4 sem customRoleId → 400
func TestUpdateAdminUserRole4NoCustomRoleID(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PATCH", "/auth/admin/users/1",
		strings.NewReader(`{"role":4}`))
	r.SetPathValue("id", "1")
	s.handleUpdateAdminUser(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// role=9 ainda é inválido
func TestUpdateAdminUserBadRoleNew(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PATCH", "/auth/admin/users/1",
		strings.NewReader(`{"role":9}`))
	r.SetPathValue("id", "1")
	s.handleUpdateAdminUser(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// shared=true sem localPart → 400 (antes do banco).
func TestCreateSharedMailboxRequiresLocalPart(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleCreateAdminUser(w, httptest.NewRequest("POST", "/auth/admin/users",
		strings.NewReader(`{"shared":true,"name":"Contato"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// shared=true com localPart inválido → 400 (antes do banco).
func TestCreateSharedMailboxBadLocalPart(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleCreateAdminUser(w, httptest.NewRequest("POST", "/auth/admin/users",
		strings.NewReader(`{"shared":true,"localPart":"con@x"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// email completo inválido (ou sem email e sem localPart) → 400 antes do banco.
func TestCreateAdminUserBadEmail(t *testing.T) {
	s := testServer(Config{})
	for _, body := range []string{
		`{"email":"sem-arroba","name":"X"}`,
		`{"email":"a@b","name":"X"}`,
		`{"email":"a b@c.com","name":"X"}`,
		`{"name":"X"}`,
		`{"email":"ok@exemplo.com","name":""}`,
	} {
		w := httptest.NewRecorder()
		s.handleCreateAdminUser(w, httptest.NewRequest("POST", "/auth/admin/users", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%s code=%d, esperado 400", body, w.Code)
		}
	}
}

// password curta (< 8 chars) no create → 400 antes do banco.
func TestCreateAdminUserPasswordTooShort(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleCreateAdminUser(w, httptest.NewRequest("POST", "/auth/admin/users",
		strings.NewReader(`{"email":"joao@exemplo.com","name":"João","password":"1234567"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// password curta (< 8 chars) no update → 400 antes do banco.
func TestUpdateAdminUserPasswordTooShort(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PATCH", "/auth/admin/users/1",
		strings.NewReader(`{"password":"curta"}`))
	r.SetPathValue("id", "1")
	s.handleUpdateAdminUser(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// send-reset: id não-numérico → 400.
func TestSendResetAdminUserBadID(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/auth/admin/users/abc/send-reset", nil)
	r.SetPathValue("id", "abc")
	s.handleSendResetAdminUser(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}

// send-reset: id não-numérico → 400 (validação pura, sem banco).
func TestSendResetAdminUserBadIDFormat(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/auth/admin/users/xyz/send-reset", nil)
	r.SetPathValue("id", "xyz")
	s.handleSendResetAdminUser(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400", w.Code)
	}
}
