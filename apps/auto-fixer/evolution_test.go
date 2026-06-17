package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendTextWithQuoted(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"key":{"id":"MSG123"}}`))
	}))
	defer srv.Close()

	c := NewEvolutionClient(srv.URL, "k", "inst")
	id, err := c.SendText(context.Background(), "120@g.us", "oi", "PARENT")
	if err != nil || id != "MSG123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if gotPath != "/message/sendText/inst" {
		t.Errorf("path=%q", gotPath)
	}
	q, ok := gotBody["quoted"].(map[string]any)
	if !ok {
		t.Fatalf("sem quoted: %v", gotBody)
	}
	key := q["key"].(map[string]any)
	if key["id"] != "PARENT" {
		t.Errorf("quoted.key.id=%v", key["id"])
	}
}

func TestSendReaction(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewEvolutionClient(srv.URL, "k", "inst")
	if err := c.SendReaction(context.Background(), "120@g.us", "MSG123", "🔧"); err != nil {
		t.Fatal(err)
	}
	key := gotBody["key"].(map[string]any)
	if key["remoteJid"] != "120@g.us" || key["fromMe"] != true || key["id"] != "MSG123" {
		t.Errorf("key errada: %v", key)
	}
	if gotBody["reaction"] != "🔧" {
		t.Errorf("reaction=%v", gotBody["reaction"])
	}
}
