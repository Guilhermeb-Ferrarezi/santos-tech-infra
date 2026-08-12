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
