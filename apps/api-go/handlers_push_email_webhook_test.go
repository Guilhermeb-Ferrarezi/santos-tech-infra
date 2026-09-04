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

func signEmailWebhookBody(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestVerifyHMACSignature cobre a mesma superfície de TestVerifyMetaSignature
// (handlers_instagram_test.go) para a função gêmea usada no webhook de email —
// autenticação de webhook é código de segurança crítico e estava sem nenhum teste.
func TestVerifyHMACSignature(t *testing.T) {
	body := []byte(`{"to":"aluno@santos-tech.com"}`)
	secret := "s3cr3t"

	if !verifyHMACSignature(body, signEmailWebhookBody(body, secret), secret) {
		t.Error("assinatura válida deveria passar")
	}
	if verifyHMACSignature(body, signEmailWebhookBody(body, "outro-secret"), secret) {
		t.Error("assinatura com secret errado não deveria passar")
	}
	if verifyHMACSignature(body, "sha256=deadbeef", secret) {
		t.Error("assinatura malformada/incorreta não deveria passar")
	}
	if verifyHMACSignature(body, "semprefixo", secret) {
		t.Error("header sem prefixo sha256= não deveria passar")
	}
	// Fail-closed: secret vazio nunca autoriza, mesmo que o header "bata" com
	// a assinatura de um corpo vazio — o caller já bloqueia com 503 antes de
	// chegar aqui nesse caso, mas a função não pode contar só com isso.
	if verifyHMACSignature(body, signEmailWebhookBody(body, ""), "") {
		t.Error("secret vazio nunca deveria validar (fail-closed)")
	}
}

func TestHandleEmailWebhookNotConfigured(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleEmailWebhook(w, httptest.NewRequest("POST", "/webhooks/email/new", strings.NewReader("{}")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("sem EmailWebhookSecret: code=%d (queria 503)", w.Code)
	}
}

func TestHandleEmailWebhookBadSignature(t *testing.T) {
	s := testServer(Config{EmailWebhookSecret: "s3cr3t"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/webhooks/email/new", strings.NewReader(`{"to":"aluno@santos-tech.com"}`))
	r.Header.Set("X-ST-Signature", "sha256=assinaturaerrada")
	s.handleEmailWebhook(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("assinatura inválida: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleEmailWebhookInvalidJSON(t *testing.T) {
	cfg := Config{EmailWebhookSecret: "s3cr3t"}
	s := testServer(cfg)
	body := []byte(`{"to":`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/webhooks/email/new", strings.NewReader(string(body)))
	r.Header.Set("X-ST-Signature", signEmailWebhookBody(body, cfg.EmailWebhookSecret))
	s.handleEmailWebhook(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("JSON inválido: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleEmailWebhookEmptyTo(t *testing.T) {
	cfg := Config{EmailWebhookSecret: "s3cr3t"}
	s := testServer(cfg)
	body := []byte(`{"to":"   "}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/webhooks/email/new", strings.NewReader(string(body)))
	r.Header.Set("X-ST-Signature", signEmailWebhookBody(body, cfg.EmailWebhookSecret))
	s.handleEmailWebhook(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("\"to\" vazio: code=%d body=%s", w.Code, w.Body.String())
	}
}
