package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func signMetaBody(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyMetaSignature(t *testing.T) {
	body := []byte(`{"object":"instagram"}`)
	secret := "s3cr3t"

	if !verifyMetaSignature(body, signMetaBody(body, secret), secret) {
		t.Error("assinatura válida deveria passar")
	}
	if verifyMetaSignature(body, signMetaBody(body, "outro-secret"), secret) {
		t.Error("assinatura com secret errado não deveria passar")
	}
	if verifyMetaSignature(body, "sha256=deadbeef", secret) {
		t.Error("assinatura malformada/incorreta não deveria passar")
	}
	if verifyMetaSignature(body, "semprefixo", secret) {
		t.Error("header sem prefixo sha256= não deveria passar")
	}
	// Fail-closed: diferente do bot-go, secret vazio NUNCA autoriza aqui —
	// o caller já bloqueia com 503 antes de chegar aqui, então aceitar tudo
	// silenciosamente seria uma regressão perigosa.
	if verifyMetaSignature(body, signMetaBody(body, ""), "") {
		t.Error("secret vazio nunca deveria validar (fail-closed)")
	}
}

func TestHandleInstagramWebhookVerify(t *testing.T) {
	s := testServer(Config{InstagramWebhookVerifyToken: "tok123"})

	// não configurado → 503
	unconfigured := testServer(Config{})
	w := httptest.NewRecorder()
	unconfigured.handleInstagramWebhookVerify(w, httptest.NewRequest("GET", "/webhooks/instagram", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("não configurado: code=%d", w.Code)
	}

	// token errado → 403
	w2 := httptest.NewRecorder()
	s.handleInstagramWebhookVerify(w2, httptest.NewRequest("GET", "/webhooks/instagram?hub.mode=subscribe&hub.verify_token=errado&hub.challenge=abc", nil))
	if w2.Code != http.StatusForbidden {
		t.Fatalf("token errado: code=%d", w2.Code)
	}

	// mode errado → 403
	w3 := httptest.NewRecorder()
	s.handleInstagramWebhookVerify(w3, httptest.NewRequest("GET", "/webhooks/instagram?hub.mode=unsubscribe&hub.verify_token=tok123&hub.challenge=abc", nil))
	if w3.Code != http.StatusForbidden {
		t.Fatalf("mode errado: code=%d", w3.Code)
	}

	// handshake correto → 200 + challenge ecoado no corpo
	w4 := httptest.NewRecorder()
	s.handleInstagramWebhookVerify(w4, httptest.NewRequest("GET", "/webhooks/instagram?hub.mode=subscribe&hub.verify_token=tok123&hub.challenge=abc123", nil))
	if w4.Code != http.StatusOK {
		t.Fatalf("handshake válido: code=%d", w4.Code)
	}
	if body := w4.Body.String(); body != "abc123" {
		t.Errorf("challenge não ecoado: body=%q", body)
	}
}

func TestHandleInstagramWebhookEventNotConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"sem app secret", Config{InstagramUserID: "1", InstagramAccessToken: "t"}},
		{"sem user id / token", Config{InstagramAppSecret: "s"}},
	}
	for _, tc := range cases {
		s := testServer(tc.cfg)
		s.instagram = newInstagramClient(tc.cfg) // NewServer faz isso em produção; testServer não
		w := httptest.NewRecorder()
		s.handleInstagramWebhookEvent(w, httptest.NewRequest("POST", "/webhooks/instagram", strings.NewReader("{}")))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: code=%d (queria 503)", tc.name, w.Code)
		}
	}
}

func TestHandleInstagramWebhookEventBadSignature(t *testing.T) {
	cfg := Config{InstagramAppSecret: "s3cr3t", InstagramUserID: "1", InstagramAccessToken: "t"}
	s := testServer(cfg)
	s.instagram = newInstagramClient(cfg)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/webhooks/instagram", strings.NewReader(`{"object":"instagram"}`))
	r.Header.Set("X-Hub-Signature-256", "sha256=assinaturaerrada")
	s.handleInstagramWebhookEvent(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("assinatura inválida: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleInstagramWebhookEventNoMatchingChanges(t *testing.T) {
	// Payload assinado corretamente, sem nenhum "change" de comentário — deve
	// responder 200 sem tentar tocar no banco (s.db é nil em testServer).
	cfg := Config{InstagramAppSecret: "s3cr3t", InstagramUserID: "1", InstagramAccessToken: "t"}
	s := testServer(cfg)
	s.instagram = newInstagramClient(cfg)
	body := []byte(`{"object":"instagram","entry":[{"id":"1","changes":[{"field":"live_comments","value":{}}]}]}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/webhooks/instagram", strings.NewReader(string(body)))
	r.Header.Set("X-Hub-Signature-256", signMetaBody(body, cfg.InstagramAppSecret))
	s.handleInstagramWebhookEvent(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestValidateInstagramCommentLinkInput(t *testing.T) {
	cases := []struct {
		name    string
		mediaID string
		in      InstagramCommentLinkInput
		wantErr bool
	}{
		{"media_id vazio", "", InstagramCommentLinkInput{URL: "https://x.com"}, true},
		{"url vazia", "123", InstagramCommentLinkInput{URL: ""}, true},
		{"url sem esquema", "123", InstagramCommentLinkInput{URL: "x.com"}, true},
		{"válido", "123", InstagramCommentLinkInput{URL: "https://santos-tech.com/blog/x"}, false},
	}
	for _, tc := range cases {
		err := validateInstagramCommentLinkInput(tc.mediaID, &tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("%s: esperava erro", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: erro inesperado: %v", tc.name, err)
		}
	}
}
