package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lookup" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"url":"http://` + r.Host + `/bytes","mime_type":"audio/ogg"}`))
			return
		}
		_, _ = w.Write([]byte("OGGDATA"))
	}))
	defer srv.Close()

	s := &WhatsAppSender{accessToken: "tok", http: srv.Client()}
	data, mime, err := s.downloadMediaFrom(context.Background(), srv.URL+"/lookup")
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if string(data) != "OGGDATA" || mime != "audio/ogg" {
		t.Fatalf("got %q %q", data, mime)
	}
}
