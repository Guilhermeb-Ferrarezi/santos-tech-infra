package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// authGuard sem cookie/Authorization retorna 401 ANTES de tocar no banco —
// mesmo padrão de TestAdminGuardNoToken (handlers_admin_users_test.go), mas
// com authGuard (não adminGuard): esta rota é pra qualquer papel autenticado.
func TestListInstitutionalMailboxesNoToken(t *testing.T) {
	s := testServer(Config{})
	h := s.authGuard(s.handleListInstitutionalMailboxes)
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/auth/mailboxes/institutional", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, esperado 401", w.Code)
	}
}
