package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestRefreshGraceReplay cobre o caso que motivou a mudança: um segundo
// request com o MESMO refresh token já consumido (retry de rede, ou corrida
// concorrente que perdeu o DELETE atômico) deve receber de volta a resposta
// já emitida — sem ser tratado como reuso malicioso.
func TestRefreshGraceReplay(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	rec := newRespRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.Header().Add("Set-Cookie", "access_token=abc; Path=/")
	rec.Header().Add("Set-Cookie", "refresh_token=def; Path=/")
	rec.WriteHeader(http.StatusOK)
	if _, err := rec.Write([]byte(`{"access_token":"abc","refresh_token":"def"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	s.cacheGraceResponse(context.Background(), "old-hash", rec)

	w := httptest.NewRecorder()
	if ok := s.tryGraceReplay(context.Background(), w, "old-hash"); !ok {
		t.Fatal("tryGraceReplay devolveu false para uma entrada que acabou de ser cacheada")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status=%d, queria %d", w.Code, http.StatusOK)
	}
	if got := w.Body.String(); got != `{"access_token":"abc","refresh_token":"def"}` {
		t.Errorf("body=%q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type=%q", got)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 2 {
		t.Errorf("esperava 2 cookies (access_token+refresh_token) replicados no replay, veio %d", len(cookies))
	}
}

// TestRefreshGraceReplayMiss garante que um hash nunca cacheado (ou já fora
// da janela) não produz um replay falso-positivo — o caller deve seguir para
// o caminho normal (tratar como possível reuso).
func TestRefreshGraceReplayMiss(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	w := httptest.NewRecorder()
	if ok := s.tryGraceReplay(context.Background(), w, "never-cached-hash"); ok {
		t.Fatal("tryGraceReplay devolveu true para um hash nunca cacheado")
	}
	if w.Code != http.StatusOK {
		// httptest.ResponseRecorder começa com 200 até algo chamar WriteHeader;
		// tryGraceReplay não deve ter escrito nada no miss.
		t.Errorf("miss não deveria ter escrito na resposta, status=%d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("miss não deveria ter escrito corpo, veio %q", w.Body.String())
	}
}

// TestRefreshGraceReplayExpires garante que a janela de graça é curta: depois
// de refreshGraceTTL, um reenvio do mesmo token volta a ser tratado como
// possível reuso (não vira uma segunda janela de reuso indefinida).
func TestRefreshGraceReplayExpires(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	rec := newRespRecorder()
	rec.WriteHeader(http.StatusOK)
	s.cacheGraceResponse(context.Background(), "old-hash", rec)

	mr.FastForward(refreshGraceTTL + time.Second)

	w := httptest.NewRecorder()
	if ok := s.tryGraceReplay(context.Background(), w, "old-hash"); ok {
		t.Fatal("tryGraceReplay devolveu true depois do TTL expirar")
	}
}

// TestRefreshGraceReplayNoRedis garante fail-safe: sem Redis configurado
// (s.rdb == nil), o replay simplesmente não acontece — não deve dar panic
// nem bloquear o fluxo normal de refresh.
func TestRefreshGraceReplayNoRedis(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	if ok := s.tryGraceReplay(context.Background(), w, "any-hash"); ok {
		t.Fatal("tryGraceReplay devolveu true sem Redis configurado")
	}
	rec := newRespRecorder()
	rec.WriteHeader(http.StatusOK)
	s.cacheGraceResponse(context.Background(), "any-hash", rec) // não deve dar panic
}
