package main

import (
	"context"
	"mime"
	"mime/multipart"
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

func TestUploadAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// O Meta rejeita o arquivo se a parte não tiver Content-Type de áudio.
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "file" && p.Header.Get("Content-Type") != "audio/ogg" {
				t.Errorf("parte 'file' com Content-Type %q, esperava audio/ogg", p.Header.Get("Content-Type"))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"MEDIA123"}`))
	}))
	defer srv.Close()

	s := &WhatsAppSender{accessToken: "tok", phoneNumberID: "PN", http: srv.Client()}
	id, err := s.uploadAudioTo(context.Background(), srv.URL, []byte("OGG"))
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if id != "MEDIA123" {
		t.Fatalf("id = %q", id)
	}
}

func TestContentToBodyAudioByID(t *testing.T) {
	id := "wa_media_id:MEDIA123"
	audio := contentToBody(MessageContent{Type: "audio", MediaURL: &id})["audio"].(map[string]any)
	if audio["id"] != "MEDIA123" {
		t.Fatalf("esperava id, got %v", audio)
	}
	if _, has := audio["link"]; has {
		t.Fatalf("não deveria ter link")
	}
	link := "https://x/a.ogg"
	a2 := contentToBody(MessageContent{Type: "audio", MediaURL: &link})["audio"].(map[string]any)
	if a2["link"] != "https://x/a.ogg" {
		t.Fatalf("esperava link")
	}
}
