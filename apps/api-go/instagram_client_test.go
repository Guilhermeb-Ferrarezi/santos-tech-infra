package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstagramClientSendPrivateReply(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Query().Get("access_token") != "tok-123" {
			t.Errorf("access_token ausente/errado na query: %q", r.URL.Query().Get("access_token"))
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"recipient_id":"1","message_id":"m1"}`))
	}))
	defer srv.Close()

	c := &instagramClient{baseURL: srv.URL, userID: "999", token: "tok-123", client: srv.Client()}
	if err := c.sendPrivateReply(context.Background(), "comment-1", "https://santos-tech.com/blog/x"); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if gotPath != "/999/messages" {
		t.Errorf("path=%q, queria /999/messages", gotPath)
	}
	recipient, _ := gotBody["recipient"].(map[string]any)
	if recipient["comment_id"] != "comment-1" {
		t.Errorf("recipient.comment_id=%v", recipient["comment_id"])
	}
	message, _ := gotBody["message"].(map[string]any)
	if message["text"] != "https://santos-tech.com/blog/x" {
		t.Errorf("message.text=%v", message["text"])
	}
}

func TestInstagramClientSendPrivateReplyErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"não foi possível encontrar o usuário solicitado"}}`))
	}))
	defer srv.Close()

	c := &instagramClient{baseURL: srv.URL, userID: "999", token: "tok-123", client: srv.Client()}
	if err := c.sendPrivateReply(context.Background(), "comment-invalido", "https://x.com"); err == nil {
		t.Fatal("esperava erro para status 400")
	}
}

func TestInstagramClientDisabled(t *testing.T) {
	c := &instagramClient{client: http.DefaultClient}
	if c.enabled() {
		t.Error("sem userID/token, enabled() deveria ser false")
	}
	if err := c.sendPrivateReply(context.Background(), "c1", "t"); err == nil {
		t.Error("client desabilitado deveria recusar o envio")
	}
}

// Carrossel só de imagens: o container PAI ainda assim é montado de forma
// assíncrona pela Meta. Publicar sem consultar o status devolve
// "Media ID is not available" (code 9007) e o post nunca vai ao ar — foi o que
// aconteceu em produção em 03/09/2026. Este teste trava a ORDEM: o status do
// pai tem que ser consultado ANTES do media_publish.
func TestInstagramClientPublishCarouselEsperaContainerPai(t *testing.T) {
	var calls []string
	mediaCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/999/media":
			mediaCalls++
			id := "child-" + string(rune('0'+mediaCalls))
			if mediaCalls == 3 { // 2 filhos, depois o pai
				id = "parent-1"
			}
			_, _ = w.Write([]byte(`{"id":"` + id + `"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/parent-1":
			_, _ = w.Write([]byte(`{"status_code":"FINISHED"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/999/media_publish":
			_, _ = w.Write([]byte(`{"id":"pub-1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &instagramClient{baseURL: srv.URL, userID: "999", token: "tok-123", client: srv.Client()}
	items := []carouselMediaItem{{URL: "https://cdn/1.png"}, {URL: "https://cdn/2.png"}}
	id, err := c.publishCarousel(context.Background(), items, "legenda")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if id != "pub-1" {
		t.Errorf("id publicado = %q, queria pub-1", id)
	}

	statusIdx, publishIdx := -1, -1
	for i, call := range calls {
		if call == "GET /parent-1" && statusIdx == -1 {
			statusIdx = i
		}
		if call == "POST /999/media_publish" && publishIdx == -1 {
			publishIdx = i
		}
	}
	if statusIdx == -1 {
		t.Fatalf("status do container pai nunca foi consultado; chamadas=%v", calls)
	}
	if publishIdx == -1 || statusIdx > publishIdx {
		t.Errorf("publicou antes de conferir o status do pai; chamadas=%v", calls)
	}
}
