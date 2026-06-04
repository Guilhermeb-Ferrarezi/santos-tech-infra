package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

const (
	sidA = "11111111-1111-1111-1111-111111111111"
	sidB = "22222222-2222-2222-2222-222222222222"
	sidC = "33333333-3333-3333-3333-333333333333"
	sidD = "44444444-4444-4444-4444-444444444444"
	sidE = "55555555-5555-5555-5555-555555555555"
	sidF = "66666666-6666-6666-6666-666666666666"
)

func TestAccountsSignParseRoundTrip(t *testing.T) {
	ids := []string{sidA, sidB}
	v := signAccountsValue("secret", ids)
	got := parseAccountsValue("secret", v)
	if !slices.Equal(got, ids) {
		t.Fatalf("round trip: got %v, want %v", got, ids)
	}
}

func TestAccountsParseRejectsTampering(t *testing.T) {
	v := signAccountsValue("secret", []string{sidA})
	// troca o payload mantendo a assinatura
	tampered := sidB + v[len(sidA):]
	if got := parseAccountsValue("secret", tampered); got != nil {
		t.Fatalf("payload adulterado deveria ser rejeitado, veio %v", got)
	}
	// segredo errado
	if got := parseAccountsValue("outro", v); got != nil {
		t.Fatalf("segredo errado deveria ser rejeitado, veio %v", got)
	}
	// lixo
	if got := parseAccountsValue("secret", "garbage"); got != nil {
		t.Fatalf("valor inválido deveria ser rejeitado, veio %v", got)
	}
}

func TestAccountsParseRejectsNonUUID(t *testing.T) {
	v := signAccountsValue("secret", []string{"not-a-uuid"})
	if got := parseAccountsValue("secret", v); got != nil {
		t.Fatalf("id não-uuid deveria ser rejeitado, veio %v", got)
	}
}

func TestReadAccountsFromRequest(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	// sem cookie → vazio
	if ids := s.readAccounts(httptest.NewRequest("GET", "/x", nil)); ids != nil {
		t.Fatalf("sem cookie: %v", ids)
	}
	// cookie válido
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: accountsCookieName, Value: signAccountsValue("secret", []string{sidA})})
	if ids := s.readAccounts(r); !slices.Equal(ids, []string{sidA}) {
		t.Fatalf("cookie válido: %v", ids)
	}
}

func TestWriteAccountsEmptyExpiresCookie(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	w := httptest.NewRecorder()
	s.writeAccounts(w, nil)
	c := w.Result().Cookies()[0]
	if c.Name != accountsCookieName || c.MaxAge >= 0 {
		t.Fatalf("lista vazia deveria expirar o cookie: %+v", c)
	}
}

func TestAppendAccountDedupesRemovesAndCaps(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})

	// anexa removendo o sid antigo (rotação de refresh)
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: accountsCookieName, Value: signAccountsValue("secret", []string{sidA, sidB})})
	w := httptest.NewRecorder()
	got := s.appendAccount(w, r, sidC, sidA)
	if !slices.Equal(got, []string{sidB, sidC}) {
		t.Fatalf("append com remove: %v", got)
	}

	// duplicata: re-anexar um sid existente o move pro fim, sem duplicar
	r2 := httptest.NewRequest("GET", "/x", nil)
	r2.AddCookie(&http.Cookie{Name: accountsCookieName, Value: signAccountsValue("secret", []string{sidA, sidB})})
	got2 := s.appendAccount(httptest.NewRecorder(), r2, sidA)
	if !slices.Equal(got2, []string{sidB, sidA}) {
		t.Fatalf("dedupe: %v", got2)
	}

	// limite de 5: estoura pelo mais antigo
	r3 := httptest.NewRequest("GET", "/x", nil)
	r3.AddCookie(&http.Cookie{Name: accountsCookieName, Value: signAccountsValue("secret", []string{sidA, sidB, sidC, sidD, sidE})})
	got3 := s.appendAccount(httptest.NewRecorder(), r3, sidF)
	if !slices.Equal(got3, []string{sidB, sidC, sidD, sidE, sidF}) {
		t.Fatalf("cap 5: %v", got3)
	}
}
