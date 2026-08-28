package main

import (
	"net/http/httptest"
	"testing"
)

// TestHandleQRLoginCreateNoStore garante que a resposta de criação do pedido
// de QR login (que carrega token+code — equivalentes a uma credencial de
// login) nunca é cacheada por proxy/navegador, no mesmo padrão já aplicado a
// login/MFA/OAuth (ver handlers_auth.go, handlers_mfa.go, handlers_oauth_provider.go).
func TestHandleQRLoginCreateNoStore(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	w := httptest.NewRecorder()
	s.handleQRLoginCreate(w, httptest.NewRequest("POST", "/public/qr-login/create", nil))

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, queria \"no-store\"", got)
	}
}
