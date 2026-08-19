package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// downloadsServer devolve um Server cujo cliente R2 aponta pra um httptest.Server
// (mesmo truque do redirectTransport de drive_test.go): dá pra exercitar o
// HeadObject sem tocar no R2 real. s.q fica nil, então todos os casos aqui têm
// que retornar ANTES de gravar a linha do catálogo — que é justamente o ponto
// destes testes.
func downloadsServer(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(Config{})
	s.r2 = newR2(Config{
		R2AccountID: "acc123",
		R2AccessKey: "key123",
		R2SecretKey: "secret123",
		R2Bucket:    "bucket",
		R2PublicURL: "https://cdn.santos-tech.com",
	})
	s.r2.http = &http.Client{Transport: &redirectTransport{base: base}}
	return s
}

func createDownloadReq(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqAs(httptest.NewRequest("POST", "/auth/admin/downloads", strings.NewReader(body)), 1)
	w := httptest.NewRecorder()
	s.handleCreateDownload(w, r)
	return w
}

const validDownloadBody = `{"name":"Instalador","kind":"file","objectKey":"downloads/abc.exe","filename":"setup.exe","contentType":"application/vnd.microsoft.portable-executable","sizeBytes":1024}`

// TestCreateDownloadExigeArquivoNoR2: os bytes vão direto pro R2 sem passar pelo
// backend, então cadastrar sem ter enviado nada deixava uma linha de catálogo
// apontando pra um objeto inexistente.
func TestCreateDownloadExigeArquivoNoR2(t *testing.T) {
	s := downloadsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if w := createDownloadReq(t, s, validDownloadBody); w.Code != 400 {
		t.Fatalf("objeto ausente no R2: code=%d, quer 400 (%s)", w.Code, w.Body.String())
	}
}

// TestCreateDownloadRecusaContentTypeDivergente: o Content-Type real do objeto
// tem que bater com o esperado pra extensão — não com o que o cliente declara.
func TestCreateDownloadRecusaContentTypeDivergente(t *testing.T) {
	s := downloadsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
	})
	if w := createDownloadReq(t, s, validDownloadBody); w.Code != 400 {
		t.Fatalf("content-type divergente: code=%d, quer 400 (%s)", w.Code, w.Body.String())
	}
}

// TestCreateDownloadRecusaTamanhoAcimaDoTeto: antes, o único tamanho validado
// era o DECLARADO no pedido da URL presigned — nada impedia subir muito mais.
func TestCreateDownloadRecusaTamanhoAcimaDoTeto(t *testing.T) {
	s := downloadsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
		w.Header().Set("Content-Length", strconv.FormatInt(maxDownloadPresignBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	})
	if w := createDownloadReq(t, s, validDownloadBody); w.Code != 400 {
		t.Fatalf("arquivo acima do teto: code=%d, quer 400 (%s)", w.Code, w.Body.String())
	}
}

// TestCreateDownloadFalhaDoR2NaoLibera: erro transitório do R2 não pode virar
// cadastro sem verificação.
func TestCreateDownloadFalhaDoR2NaoLibera(t *testing.T) {
	s := downloadsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if w := createDownloadReq(t, s, validDownloadBody); w.Code != 502 {
		t.Fatalf("R2 fora do ar: code=%d, quer 502 (%s)", w.Code, w.Body.String())
	}
}

// TestCreateDownloadSemR2 cobre a degradação quando o R2 não está configurado.
func TestCreateDownloadSemR2(t *testing.T) {
	s := testServer(Config{})
	if w := createDownloadReq(t, s, validDownloadBody); w.Code != 503 {
		t.Fatalf("sem R2: code=%d, quer 503 (%s)", w.Code, w.Body.String())
	}
}

// TestDownloadsPresignExpiryCurta: mesma janela curta do upload de vídeo.
func TestDownloadsPresignExpiryCurta(t *testing.T) {
	s := downloadsServer(t, func(w http.ResponseWriter, r *http.Request) {})
	r := reqAs(httptest.NewRequest("POST", "/auth/admin/downloads/presign",
		strings.NewReader(`{"filename":"setup.exe","size":1024}`)), 1)
	w := httptest.NewRecorder()
	s.handleDownloadsPresign(w, r)
	if w.Code != 200 {
		t.Fatalf("code=%d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "X-Amz-Expires="+strconv.Itoa(int(presignExpiry.Seconds()))) {
		t.Errorf("uploadUrl sem a expiração curta esperada: %s", w.Body.String())
	}
}
