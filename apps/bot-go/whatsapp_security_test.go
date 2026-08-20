package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestValidateMetaHMACFailClosed(t *testing.T) {
	body := []byte(`{"entry":[]}`)

	// Sem segredo, NADA passa — nem uma assinatura bem formada.
	mac := hmac.New(sha256.New, []byte("qualquer"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if ValidateMetaHMAC(body, sig, "") {
		t.Error("segredo vazio deveria recusar (fail-closed)")
	}
	if ValidateMetaHMAC(body, "", "") {
		t.Error("segredo vazio + assinatura vazia deveria recusar")
	}

	// Com segredo correto, a assinatura correta passa e a errada não.
	mac = hmac.New(sha256.New, []byte("segredo"))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !ValidateMetaHMAC(body, good, "segredo") {
		t.Error("assinatura válida deveria passar")
	}
	if ValidateMetaHMAC(body, good, "outro-segredo") {
		t.Error("assinatura de outro segredo não deveria passar")
	}
}

// Um mediaID vindo do payload não pode redirecionar o Bearer token da Meta
// para outro host.
func TestDownloadMediaRejeitaMediaIDNaoNumerico(t *testing.T) {
	var chamou bool
	s := &WhatsAppSender{
		accessToken: "token-secreto",
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			chamou = true
			return nil, http.ErrUseLastResponse
		})},
	}

	for _, id := range []string{"@evil.com/x", "../../x", "123/../../y", "", "abc", "12 3", "1%2e"} {
		_, _, err := s.DownloadMedia(context.Background(), id)
		if err == nil {
			t.Errorf("mediaID %q deveria ser recusado", id)
		} else if !strings.Contains(err.Error(), "media id inválido") {
			t.Errorf("mediaID %q: erro inesperado: %v", id, err)
		}
	}
	if chamou {
		t.Error("nenhuma request deveria ter saído com mediaID inválido")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
