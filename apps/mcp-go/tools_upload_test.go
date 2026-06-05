package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PNG 1x1 válido — o /auth/upload detecta o formato pelo conteúdo.
var tinyPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
}

func TestUploadImageEnviaMultipart(t *testing.T) {
	var gotAuth, gotContentType string
	var gotFile []byte
	fakeAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if r.URL.Path != "/auth/upload" || r.Method != "POST" {
			w.WriteHeader(404)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		defer f.Close()
		gotFile, _ = io.ReadAll(f)
		w.Write([]byte(`{"url":"https://cdn.santos-tech.com/uploads/1/abc.png"}`))
	}))
	defer fakeAuth.Close()

	session := newTestSession(t, Config{AuthBaseURL: fakeAuth.URL}, nil, "Bearer st_up")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "upload_image",
		Arguments: map[string]any{"imageBase64": base64.StdEncoding.EncodeToString(tinyPNG)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool falhou: %s", toolText(t, res))
	}
	if gotAuth != "Bearer st_up" {
		t.Fatalf("Authorization não repassado: %q", gotAuth)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Fatalf("Content-Type não é multipart: %q", gotContentType)
	}
	if string(gotFile) != string(tinyPNG) {
		t.Fatalf("conteúdo do arquivo difere (%d bytes vs %d)", len(gotFile), len(tinyPNG))
	}
	if !strings.Contains(toolText(t, res), "cdn.santos-tech.com") {
		t.Fatalf("URL não devolvida: %s", toolText(t, res))
	}
}

func TestUploadImageAceitaDataURI(t *testing.T) {
	fakeAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"url":"https://cdn.santos-tech.com/x.png"}`))
	}))
	defer fakeAuth.Close()

	session := newTestSession(t, Config{AuthBaseURL: fakeAuth.URL}, nil, "Bearer st_up")
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "upload_image",
		Arguments: map[string]any{"imageBase64": dataURI},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("data URI deveria ser aceito: %s", toolText(t, res))
	}
}

func TestUploadImageBase64Invalido(t *testing.T) {
	session := newTestSession(t, Config{}, nil, "Bearer st_up")
	for name, arg := range map[string]string{
		"base64 inválido": "isto não é base64!!!",
		"vazio":           "",
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "upload_image",
			Arguments: map[string]any{"imageBase64": arg},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Errorf("%s: esperava isError", name)
		}
	}
}
