package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func presignServer() *Server {
	s := testServer(Config{})
	s.r2 = newR2(Config{
		R2AccountID: "acc123",
		R2AccessKey: "key123",
		R2SecretKey: "secret123",
		R2Bucket:    "bucket",
		R2PublicURL: "https://cdn.santos-tech.com",
	})
	return s
}

func presignReq(t *testing.T, body presignUploadRequest) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest("POST", "/auth/upload/presigned", bytes.NewReader(b))
	r = reqAs(r, 42)
	w := httptest.NewRecorder()
	presignServer().handleVideoPresign(w, r)
	return w
}

func TestHandleVideoPresignDisabled(t *testing.T) {
	s := testServer(Config{}) // sem R2 configurado
	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/auth/upload/presigned", nil), 1)
	s.handleVideoPresign(w, r)
	if w.Code != 503 {
		t.Fatalf("esperava 503 sem R2, veio %d", w.Code)
	}
}

func TestHandleVideoPresignUnsupportedType(t *testing.T) {
	w := presignReq(t, presignUploadRequest{Filename: "aula.avi", ContentType: "video/x-msvideo", Size: 1024})
	if w.Code != 400 {
		t.Fatalf("esperava 400 pra tipo não suportado, veio %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleVideoPresignInvalidSize(t *testing.T) {
	cases := []int64{0, -1, maxPresignVideoBytes + 1}
	for _, size := range cases {
		w := presignReq(t, presignUploadRequest{Filename: "aula.mp4", ContentType: "video/mp4", Size: size})
		if w.Code != 400 {
			t.Errorf("size=%d: esperava 400, veio %d", size, w.Code)
		}
	}
}

func TestHandleVideoPresignSuccess(t *testing.T) {
	w := presignReq(t, presignUploadRequest{Filename: "aula.mp4", ContentType: "video/mp4", Size: 10 << 20})
	if w.Code != 200 {
		t.Fatalf("esperava 200, veio %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		UploadURL string `json:"uploadUrl"`
		PublicURL string `json:"publicUrl"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resposta: %v", err)
	}
	if !strings.HasPrefix(resp.PublicURL, "https://cdn.santos-tech.com/uploads/42/") || !strings.HasSuffix(resp.PublicURL, ".mp4") {
		t.Errorf("publicUrl inesperada: %s", resp.PublicURL)
	}
	u, err := url.Parse(resp.UploadURL)
	if err != nil {
		t.Fatalf("uploadUrl inválida: %v", err)
	}
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Error("uploadUrl sem X-Amz-Signature")
	}
	if !strings.Contains(u.Path, "/bucket/uploads/42/") {
		t.Errorf("uploadUrl com path inesperado: %s", u.Path)
	}
}

func TestR2PresignPut(t *testing.T) {
	r := newR2(Config{
		R2AccountID: "acc123",
		R2AccessKey: "key123",
		R2SecretKey: "secret123",
		R2Bucket:    "bucket",
		R2PublicURL: "https://cdn.santos-tech.com",
	})
	got := r.PresignPut("uploads/1/abc.mp4", 15*time.Minute)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("URL inválida: %v", err)
	}
	if u.Host != "acc123.r2.cloudflarestorage.com" {
		t.Errorf("host inesperado: %s", u.Host)
	}
	if u.Path != "/bucket/uploads/1/abc.mp4" {
		t.Errorf("path inesperado: %s", u.Path)
	}
	q := u.Query()
	if q.Get("X-Amz-Expires") != "900" {
		t.Errorf("X-Amz-Expires = %q, quer \"900\"", q.Get("X-Amz-Expires"))
	}
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		t.Errorf("algoritmo inesperado: %s", q.Get("X-Amz-Algorithm"))
	}
	if len(q.Get("X-Amz-Signature")) != 64 {
		t.Errorf("assinatura com tamanho inesperado: %d", len(q.Get("X-Amz-Signature")))
	}
}
