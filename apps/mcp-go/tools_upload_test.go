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
	for name, args := range map[string]map[string]any{
		"base64 inválido": {"imageBase64": "isto não é base64!!!"},
		"sem nada":        {},
		"os dois":         {"imageBase64": "aGk=", "imageUrl": "https://x.com/a.png"},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "upload_image",
			Arguments: args,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Errorf("%s: esperava isError", name)
		}
	}
}

func TestUploadImagePorURL(t *testing.T) {
	// Servidor que faz os dois papéis: serve a imagem e recebe o upload.
	var gotFile []byte
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/imagem.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(tinyPNG)
		case "/auth/upload":
			f, _, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(400)
				return
			}
			defer f.Close()
			gotFile, _ = io.ReadAll(f)
			w.Write([]byte(`{"url":"https://cdn.santos-tech.com/uploads/1/viaurl.png"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()

	s := NewServer(Config{AuthBaseURL: fake.URL}, nil, nil)
	s.fetch = http.DefaultClient // httptest é 127.0.0.1 — o guard anti-SSRF bloquearia
	session := newTestSessionFor(t, s, "Bearer st_up")

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "upload_image",
		Arguments: map[string]any{"imageUrl": fake.URL + "/imagem.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool falhou: %s", toolText(t, res))
	}
	if string(gotFile) != string(tinyPNG) {
		t.Fatalf("conteúdo baixado difere (%d bytes vs %d)", len(gotFile), len(tinyPNG))
	}
	if !strings.Contains(toolText(t, res), "viaurl.png") {
		t.Fatalf("URL não devolvida: %s", toolText(t, res))
	}
}

func TestFetchClientBloqueiaIPPrivado(t *testing.T) {
	// O guard anti-SSRF deve recusar loopback/privados — o MCP roda dentro da
	// rede da infra e não pode virar proxy para serviços internos.
	s := NewServer(Config{}, nil, nil)
	for _, target := range []string{"http://127.0.0.1:5432/x", "http://10.0.0.1/x", "http://localhost/x"} {
		_, errMsg := s.fetchImage(context.Background(), target)
		if errMsg == "" {
			t.Errorf("%s: deveria ser bloqueado", target)
		}
	}
}
