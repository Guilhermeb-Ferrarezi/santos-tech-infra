package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func testServer(cfg Config) *Server { return &Server{cfg: cfg} }

// testServerWithRedis devolve um Server com um Redis fake (miniredis) para os
// testes de handlers que dependem do rdb (ex.: state do OAuth). O miniredis é
// encerrado via t.Cleanup.
func testServerWithRedis(t *testing.T, cfg Config) *Server {
	t.Helper()
	mr := miniredis.RunT(t)
	s := testServer(cfg)
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })
	return s
}

func TestClearAuthCookies(t *testing.T) {
	s := testServer(Config{CookieDomain: "santos-tech.com"})
	w := httptest.NewRecorder()
	s.clearAuthCookies(w)
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("esperava 2 cookies, veio %d", len(cookies))
	}
	for _, c := range cookies {
		if c.MaxAge >= 0 {
			t.Errorf("cookie %s MaxAge=%d (devia ser < 0)", c.Name, c.MaxAge)
		}
		if c.Domain != "santos-tech.com" {
			t.Errorf("cookie %s Domain=%q", c.Name, c.Domain)
		}
	}
}

func TestSetCookieSecure(t *testing.T) {
	prod := testServer(Config{Production: true})
	w := httptest.NewRecorder()
	prod.setCookie(w, "x", "y", 60)
	if !w.Result().Cookies()[0].Secure {
		t.Error("produção deveria setar Secure")
	}
	dev := testServer(Config{Production: false})
	w2 := httptest.NewRecorder()
	dev.setCookie(w2, "x", "y", 60)
	if w2.Result().Cookies()[0].Secure {
		t.Error("dev não deveria setar Secure")
	}
}

func TestAuthGuard(t *testing.T) {
	cfg := Config{JWTSecret: "s-access", JWTRefreshSecret: "s-refresh"}
	s := testServer(cfg)
	var gotUID int64 = -1
	h := s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		gotUID = userIDFrom(r)
		w.WriteHeader(http.StatusOK)
	})

	// sem token → 401
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/x", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("sem token: code=%d", w.Code)
	}

	access, _, _ := generateTokens(cfg.JWTSecret, cfg.JWTRefreshSecret, 77, "a@b.com", "")

	// cookie válido → 200 + uid no contexto
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: "access_token", Value: access})
	w2 := httptest.NewRecorder()
	h(w2, r)
	if w2.Code != http.StatusOK || gotUID != 77 {
		t.Fatalf("cookie válido: code=%d uid=%d", w2.Code, gotUID)
	}

	// Authorization: Bearer válido → 200 + uid
	gotUID = -1
	r2 := httptest.NewRequest("GET", "/x", nil)
	r2.Header.Set("Authorization", "Bearer "+access)
	w3 := httptest.NewRecorder()
	h(w3, r2)
	if w3.Code != http.StatusOK || gotUID != 77 {
		t.Fatalf("bearer: code=%d uid=%d", w3.Code, gotUID)
	}
}

func TestCORS(t *testing.T) {
	s := testServer(Config{CORSOrigins: []string{"https://mails.santos-tech.com"}})
	h := s.cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// origem permitida → reflete Origin + credenciais
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Origin", "https://mails.santos-tech.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "https://mails.santos-tech.com" {
		t.Error("origem permitida sem Access-Control-Allow-Origin")
	}
	if w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("faltou Access-Control-Allow-Credentials")
	}

	// origem proibida → sem header
	r2 := httptest.NewRequest("GET", "/x", nil)
	r2.Header.Set("Origin", "https://evil.com")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("origem proibida recebeu Access-Control-Allow-Origin")
	}

	// preflight → 204
	r3 := httptest.NewRequest("OPTIONS", "/x", nil)
	r3.Header.Set("Origin", "https://mails.santos-tech.com")
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, r3)
	if w3.Code != http.StatusNoContent {
		t.Errorf("preflight code=%d", w3.Code)
	}
}

func TestCORSFailClosed(t *testing.T) {
	s := testServer(Config{}) // sem CORSOrigins nem AuthWebOrigin
	h := s.cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// allowlist vazia → nenhuma origem refletida (fail-closed)
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("allowlist vazia não devia refletir nenhuma origem")
	}
	if w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("allowlist vazia não devia setar Allow-Credentials")
	}
}

func TestCORSAuthWebOrigin(t *testing.T) {
	s := testServer(Config{
		CORSOrigins:   []string{"https://mails.santos-tech.com"},
		AuthWebOrigin: "https://auth.santos-tech.com",
	})
	h := s.cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// AuthWebOrigin deve ser permitida mesmo não estando em CORSOrigins
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Origin", "https://auth.santos-tech.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "https://auth.santos-tech.com" {
		t.Error("AuthWebOrigin deveria ser refletida")
	}
}

func TestAllowedRedirect(t *testing.T) {
	s := testServer(Config{
		CORSOrigins:   []string{"https://mails.santos-tech.com", "https://app.santos-tech.com"},
		AuthWebOrigin: "https://auth.santos-tech.com",
	})
	for _, u := range []string{
		"https://mails.santos-tech.com/leads",
		"https://auth.santos-tech.com",
		"https://app.santos-tech.com/x?y=1",
	} {
		if !s.allowedRedirect(u) {
			t.Errorf("devia permitir %q", u)
		}
	}
	for _, u := range []string{
		"https://evil.com",
		"https://mails.santos-tech.com.evil.com/x", // host diferente (não é prefixo seguro)
		"http://mails.santos-tech.com",             // scheme diferente
		"//evil.com",
		"javascript:alert(1)",
		"",
	} {
		if s.allowedRedirect(u) {
			t.Errorf("NÃO devia permitir %q", u)
		}
	}
}

func TestHandleLogoutGet(t *testing.T) {
	cfg := Config{
		CookieDomain:  "santos-tech.com",
		AuthWebOrigin: "https://auth.santos-tech.com",
		CORSOrigins:   []string{"https://mails.santos-tech.com"},
	}
	s := testServer(cfg)

	// sem redirect → AuthWebOrigin + limpa 2 cookies
	w := httptest.NewRecorder()
	s.handleLogoutGet(w, httptest.NewRequest("GET", "/auth/logout", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("code=%d (queria 302)", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != cfg.AuthWebOrigin {
		t.Errorf("Location=%q", loc)
	}
	if len(w.Result().Cookies()) != 2 {
		t.Error("deveria limpar 2 cookies")
	}

	// redirect permitido → vai pro destino completo
	w2 := httptest.NewRecorder()
	s.handleLogoutGet(w2, httptest.NewRequest("GET", "/auth/logout?redirect=https://mails.santos-tech.com/x", nil))
	if loc := w2.Header().Get("Location"); loc != "https://mails.santos-tech.com/x" {
		t.Errorf("redirect permitido: Location=%q", loc)
	}

	// redirect proibido → fallback pro AuthWebOrigin
	w3 := httptest.NewRecorder()
	s.handleLogoutGet(w3, httptest.NewRequest("GET", "/auth/logout?redirect=https://evil.com", nil))
	if loc := w3.Header().Get("Location"); loc != cfg.AuthWebOrigin {
		t.Errorf("redirect proibido deveria cair no fallback, Location=%q", loc)
	}
}
