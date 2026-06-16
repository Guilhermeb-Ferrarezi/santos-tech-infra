package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// testGoogleConfig returns a minimal oauth2.Config sufficient to pass the non-nil check
// inside handleGoogleCallback without making real network calls.
func testGoogleConfig() *oauth2.Config { return &oauth2.Config{ClientID: "test-client"} }

// assertCallbackRedirect asserts the response is a redirect to <origin>/?error=<errCode>.
func assertCallbackRedirect(t *testing.T, w *httptest.ResponseRecorder, origin, errCode string) {
	t.Helper()
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", w.Code)
	}
	want := origin + "/?error=" + errCode
	if loc := w.Header().Get("Location"); loc != want {
		t.Errorf("Location = %q, want %q", loc, want)
	}
}

// TestGoogleCallbackErrorFromGoogle verifies that a ?error= param from Google
// redirects immediately to oauth_denied without any state/Redis checks.
func TestGoogleCallbackErrorFromGoogle(t *testing.T) {
	s := testServerWithRedis(t, Config{AuthWebOrigin: "https://auth.santos-tech.com"})
	req := httptest.NewRequest("GET", "/auth/google/callback?error=access_denied", nil)
	w := httptest.NewRecorder()
	s.handleGoogleCallback(w, req)
	assertCallbackRedirect(t, w, "https://auth.santos-tech.com", "oauth_denied")
}

// TestGoogleCallbackOAuthNotConfigured verifies oauth_failed when Google OAuth is disabled
// (GOOGLE_CLIENT_ID not set → s.google == nil).
func TestGoogleCallbackOAuthNotConfigured(t *testing.T) {
	s := testServerWithRedis(t, Config{AuthWebOrigin: "https://auth.santos-tech.com"})
	// s.google is nil by default
	req := httptest.NewRequest("GET", "/auth/google/callback?state=abc&code=xyz", nil)
	w := httptest.NewRecorder()
	s.handleGoogleCallback(w, req)
	assertCallbackRedirect(t, w, "https://auth.santos-tech.com", "oauth_failed")
}

// TestGoogleCallbackNoCookie verifies oauth_failed when the oauth_state cookie is absent.
func TestGoogleCallbackNoCookie(t *testing.T) {
	s := testServerWithRedis(t, Config{AuthWebOrigin: "https://auth.santos-tech.com"})
	s.google = testGoogleConfig()
	req := httptest.NewRequest("GET", "/auth/google/callback?state=somestate&code=xyz", nil)
	w := httptest.NewRecorder()
	s.handleGoogleCallback(w, req)
	assertCallbackRedirect(t, w, "https://auth.santos-tech.com", "oauth_failed")
}

// TestGoogleCallbackStateMismatch verifies that a cookie state that does not match the
// URL ?state= param is rejected, even when a valid Redis key for the cookie state exists.
func TestGoogleCallbackStateMismatch(t *testing.T) {
	s := testServerWithRedis(t, Config{AuthWebOrigin: "https://auth.santos-tech.com"})
	s.google = testGoogleConfig()
	cookieState := "aabb11cc22dd"
	s.rdb.Set(context.Background(), oauthStateKey(cookieState), "1", time.Minute)

	req := httptest.NewRequest("GET", "/auth/google/callback?state=totallydifferent&code=xyz", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: cookieState})
	w := httptest.NewRecorder()
	s.handleGoogleCallback(w, req)
	assertCallbackRedirect(t, w, "https://auth.santos-tech.com", "oauth_failed")
}

// TestGoogleCallbackStateNotInRedis verifies that a state which matches cookie and URL param
// but was never stored in Redis (or already expired) is rejected.
func TestGoogleCallbackStateNotInRedis(t *testing.T) {
	s := testServerWithRedis(t, Config{AuthWebOrigin: "https://auth.santos-tech.com"})
	s.google = testGoogleConfig()
	state := "notinredis9999"
	// Intentionally do NOT set the state in Redis.
	req := httptest.NewRequest("GET", "/auth/google/callback?state="+state+"&code=xyz", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	w := httptest.NewRecorder()
	s.handleGoogleCallback(w, req)
	assertCallbackRedirect(t, w, "https://auth.santos-tech.com", "oauth_failed")
}

// TestGoogleCallbackStateOneTimeUse verifies the one-time-use property of the OAuth state:
// the Redis key is consumed on the first valid request so a replay is rejected.
func TestGoogleCallbackStateOneTimeUse(t *testing.T) {
	s := testServerWithRedis(t, Config{AuthWebOrigin: "https://auth.santos-tech.com"})
	s.google = testGoogleConfig()
	state := "onetimeuse1234"
	ctx := context.Background()
	s.rdb.Set(ctx, oauthStateKey(state), "1", time.Minute)

	// First call: state is valid; no code param so it fails after consuming the key.
	req1 := httptest.NewRequest("GET", "/auth/google/callback?state="+state, nil)
	req1.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	w1 := httptest.NewRecorder()
	s.handleGoogleCallback(w1, req1)
	assertCallbackRedirect(t, w1, "https://auth.santos-tech.com", "oauth_failed")

	// The state must have been removed from Redis.
	n, _ := s.rdb.Exists(ctx, oauthStateKey(state)).Result()
	if n != 0 {
		t.Error("oauth state should have been consumed (deleted from Redis) after first use")
	}

	// Second call with the same state and a code → Redis Del returns 0; replay rejected.
	req2 := httptest.NewRequest("GET", "/auth/google/callback?state="+state+"&code=xyz", nil)
	req2.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	w2 := httptest.NewRecorder()
	s.handleGoogleCallback(w2, req2)
	assertCallbackRedirect(t, w2, "https://auth.santos-tech.com", "oauth_failed")
}
