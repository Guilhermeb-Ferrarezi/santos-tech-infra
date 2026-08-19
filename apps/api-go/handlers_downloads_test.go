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

// ── /public/downloads: credencial do Santos Hub ─────────────────────────────

func publicDownloadsReq(s *Server, header, value string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/public/downloads", nil)
	if header != "" {
		r.Header.Set(header, value)
	}
	w := httptest.NewRecorder()
	s.santosHubGuard(s.handleListPublicDownloads)(w, r)
	return w
}

// TestPublicDownloadsSemTokenConfigurado: fail-closed. Sem SANTOS_HUB_TOKEN a
// rota não pode "liberar porque não tem token configurado" — ela entrega
// instaladores .exe/.msi/.ps1/.bat.
func TestPublicDownloadsSemTokenConfigurado(t *testing.T) {
	s := testServer(Config{})
	if w := publicDownloadsReq(s, "X-Santos-Hub-Token", "qualquer"); w.Code != 503 {
		t.Fatalf("code=%d, quer 503 (%s)", w.Code, w.Body.String())
	}
}

// TestPublicDownloadsExigeCredencial: era público, sem autenticação nenhuma.
func TestPublicDownloadsExigeCredencial(t *testing.T) {
	s := testServer(Config{SantosHubToken: "segredo-do-hub"})
	casos := []struct{ header, value string }{
		{"", ""},
		{"X-Santos-Hub-Token", ""},
		{"X-Santos-Hub-Token", "errado"},
		{"Authorization", "Bearer errado"},
		{"X-Santos-Hub-Token", "segredo-do-hub-mais-coisa"},
	}
	for _, c := range casos {
		if w := publicDownloadsReq(s, c.header, c.value); w.Code != 401 {
			t.Errorf("%s=%q: code=%d, quer 401", c.header, c.value, w.Code)
		}
	}
}

// TestPublicDownloadsAceitaOsDoisHeaders: o Hub pode mandar em qualquer um dos
// dois formatos. Passa do guard e morre no banco (s.q == nil), o que já prova
// que a credencial foi aceita.
func TestPublicDownloadsAceitaOsDoisHeaders(t *testing.T) {
	s := testServer(Config{SantosHubToken: "segredo-do-hub"})
	for _, c := range []struct{ header, value string }{
		{"X-Santos-Hub-Token", "segredo-do-hub"},
		{"Authorization", "Bearer segredo-do-hub"},
	} {
		func() {
			defer func() { _ = recover() }() // s.q == nil: o handler entra no banco
			if w := publicDownloadsReq(s, c.header, c.value); w.Code == 401 || w.Code == 503 {
				t.Errorf("%s: credencial válida recusada (code=%d)", c.header, w.Code)
			}
		}()
	}
}

// TestR2PresignGet: o link do catálogo deixou de ser a URL pública permanente
// do R2 (uma vez vista, valia pra sempre e pra qualquer um) e virou uma URL
// SigV4 de curta duração. GET não manda Content-Type, então só "host" é
// assinado.
func TestR2PresignGet(t *testing.T) {
	r := newR2(Config{
		R2AccountID: "acc123",
		R2AccessKey: "key123",
		R2SecretKey: "secret123",
		R2Bucket:    "bucket",
		R2PublicURL: "https://cdn.santos-tech.com",
	})
	u, err := url.Parse(r.PresignGet("downloads/abc.exe", downloadSignedURLTTL))
	if err != nil {
		t.Fatalf("URL inválida: %v", err)
	}
	if u.Host != "acc123.r2.cloudflarestorage.com" || u.Path != "/bucket/downloads/abc.exe" {
		t.Errorf("host/path inesperados: %s%s", u.Host, u.Path)
	}
	q := u.Query()
	if q.Get("X-Amz-SignedHeaders") != "host" {
		t.Errorf("SignedHeaders = %q, quer \"host\"", q.Get("X-Amz-SignedHeaders"))
	}
	if q.Get("X-Amz-Expires") != strconv.Itoa(int(downloadSignedURLTTL.Seconds())) {
		t.Errorf("X-Amz-Expires = %q", q.Get("X-Amz-Expires"))
	}
	if len(q.Get("X-Amz-Signature")) != 64 {
		t.Errorf("assinatura com tamanho inesperado: %d", len(q.Get("X-Amz-Signature")))
	}
	// Assinaturas de GET e PUT pra mesma key têm que diferir (o método entra na
	// canonical request) — senão um link de leitura viraria um de escrita.
	pu, err := url.Parse(r.PresignPut("downloads/abc.exe", "application/x-msi", downloadSignedURLTTL))
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("X-Amz-Signature") == pu.Query().Get("X-Amz-Signature") {
		t.Error("GET e PUT com a mesma assinatura — o método não está sendo assinado")
	}
}
