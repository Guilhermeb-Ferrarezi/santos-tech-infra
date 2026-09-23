package main

// Testes do download de imageUrl (quiz_image_fetch.go). Duas camadas
// testadas separadamente, de propósito:
//
//   - A camada de SSRF (isBlockedQuizImageIP, newQuizImageDialContext,
//     quizImageCheckRedirect) é testada SEM rede nenhuma: os casos de
//     rejeição (IP literal privado, host que resolve pra IP privado via um
//     resolvedor FAKE injetado) são detectados antes de qualquer syscall
//     connect(), então rodam determinísticos em qualquer ambiente.
//   - A camada de conteúdo (downloadQuizImage: tamanho, Content-Type) é
//     testada com httptest.Server + um *http.Client comum (srv.Client()),
//     SEM passar pelo dialer de SSRF — httptest só escuta em loopback
//     (127.0.0.1), que o dialer de produção bloqueia de propósito, então
//     testar a validação de CONTEÚDO exige não exercitar a validação de IP
//     na mesma chamada (são unidades diferentes; a de IP já está coberta
//     acima).
//
// Um caso pedido no plano não tem teste de ponta a ponta aqui:
// "redirecionamento de URL PÚBLICA para IP privado" exigiria uma conexão
// real bem-sucedida a um servidor alcançável de fora do loopback (pra
// receber o 3xx) — não dá pra simular com httptest (loopback) nem com um
// resolvedor fake (o problema é chegar na primeira resposta, não resolver
// nome). A proteção nesse caso é estrutural, não uma checagem própria: o
// *http.Client reusa o MESMO Transport/DialContext pra seguir um redirect,
// então o destino do redirect passa pela EXATA validação exercida abaixo em
// TestNewQuizImageDialContextRejeita (host que resolve pra IP privado) — o
// redirect só decide PRA ONDE ir; quem decide SE pode conectar é sempre o
// dialer, testado isoladamente.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// pngMagicBytes: só a assinatura PNG (8 bytes) — suficiente pra
// http.DetectContentType reconhecer "image/png" via sniffing; não precisa
// ser um PNG válido de verdade pros testes de Content-Type.
var pngMagicBytes = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

// ── isBlockedQuizImageIP ─────────────────────────────────────────────────

func TestIsBlockedQuizImageIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",  // loopback v4
		"127.0.0.53", // loopback v4, qualquer endereço da faixa
		"10.0.0.1",   // privada
		"10.255.255.255",
		"172.16.0.1", // privada
		"172.31.255.254",
		"192.168.1.1",     // privada
		"169.254.169.254", // metadados de nuvem (AWS/GCP/Azure)
		"169.254.0.1",     // link-local v4
		"100.64.0.1",      // CGNAT
		"100.127.255.255",
		"0.0.0.0",   // não especificado
		"0.0.0.5",   // faixa 0.0.0.0/8
		"224.0.0.1", // multicast
		"::1",       // loopback v6
		"fc00::1",   // unique local v6
		"fd00::1",   // unique local v6 (subfaixa de fc00::/7)
		"fe80::1",   // link-local v6
		"::",        // não especificado v6
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("IP de teste inválido: %s", s)
		}
		if !isBlockedQuizImageIP(ip) {
			t.Errorf("isBlockedQuizImageIP(%s) = false, queria true (bloqueado)", s)
		}
	}

	allowed := []string{
		"8.8.8.8",              // público
		"1.1.1.1",              // público
		"93.184.216.34",        // público (example.com, histórico)
		"2606:4700:4700::1111", // público v6 (Cloudflare)
	}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("IP de teste inválido: %s", s)
		}
		if isBlockedQuizImageIP(ip) {
			t.Errorf("isBlockedQuizImageIP(%s) = true, queria false (público, permitido)", s)
		}
	}
}

// ── newQuizImageDialContext — SSRF, sem rede real ───────────────────────

// TestNewQuizImageDialContextIPLiteralPrivadoRejeitado cobre "URL com IP
// privado literal → rejeitada": o host já chega como IP (net.ParseIP não
// devolve nil), então o dial nem tenta resolver — rejeita antes de discar.
func TestNewQuizImageDialContextIPLiteralPrivadoRejeitado(t *testing.T) {
	dial := newQuizImageDialContext(nil)
	casos := []string{"10.0.0.5:80", "127.0.0.1:80", "169.254.169.254:80", "192.168.1.1:443"}
	for _, addr := range casos {
		_, err := dial(context.Background(), "tcp", addr)
		if !errors.Is(err, errQuizImagemURLIndisponivel) {
			t.Errorf("dial(%s) = %v, queria errQuizImagemURLIndisponivel", addr, err)
		}
	}
}

// TestNewQuizImageDialContextLocalhostRejeitado cobre "URL com localhost →
// rejeitada". Usa um resolvedor FAKE (localhost → 127.0.0.1) em vez do
// resolvedor real do SO, pra o teste não depender de como /etc/hosts ou o
// resolvedor do ambiente de CI resolve "localhost".
func TestNewQuizImageDialContextLocalhostRejeitado(t *testing.T) {
	fakeResolve := func(ctx context.Context, host string) ([]net.IP, error) {
		if host == "localhost" {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return nil, errors.New("host inesperado no teste")
	}
	dial := newQuizImageDialContext(fakeResolve)
	_, err := dial(context.Background(), "tcp", "localhost:80")
	if !errors.Is(err, errQuizImagemURLIndisponivel) {
		t.Errorf("dial(localhost:80) = %v, queria errQuizImagemURLIndisponivel", err)
	}
}

// TestNewQuizImageDialContextHostResolveParaPrivadoRejeitado cobre "host que
// resolve para 127.0.0.1 → rejeitado" — e, por extensão, é a MESMA validação
// que protegeria contra "redirecionamento de URL pública para IP privado"
// (ver o comentário no topo do arquivo): o destino de um redirect passa por
// este exato dial. O resolvedor é injetado (fake) — nenhuma consulta DNS
// real acontece.
func TestNewQuizImageDialContextHostResolveParaPrivadoRejeitado(t *testing.T) {
	fakeResolve := func(ctx context.Context, host string) ([]net.IP, error) {
		if host == "interno.exemplo.com" {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		if host == "metadados.exemplo.com" {
			return []net.IP{net.ParseIP("169.254.169.254")}, nil
		}
		return nil, errors.New("host inesperado no teste")
	}
	dial := newQuizImageDialContext(fakeResolve)
	for _, host := range []string{"interno.exemplo.com", "metadados.exemplo.com"} {
		_, err := dial(context.Background(), "tcp", host+":80")
		if !errors.Is(err, errQuizImagemURLIndisponivel) {
			t.Errorf("dial(%s:80) = %v, queria errQuizImagemURLIndisponivel", host, err)
		}
	}
}

// TestNewQuizImageDialContextRejeitaSeQualquerIPResolvidoForPrivado cobre a
// regra "REJEITE se QUALQUER IP resolvido for privado" — mesmo quando a
// lista tem um IP público primeiro, um único IP privado no meio já derruba
// a resolução inteira (não basta o primeiro da lista passar).
func TestNewQuizImageDialContextRejeitaSeQualquerIPResolvidoForPrivado(t *testing.T) {
	fakeResolve := func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("10.0.0.1")}, nil
	}
	dial := newQuizImageDialContext(fakeResolve)
	_, err := dial(context.Background(), "tcp", "multi.exemplo.com:80")
	if !errors.Is(err, errQuizImagemURLIndisponivel) {
		t.Errorf("dial com IP privado na lista = %v, queria errQuizImagemURLIndisponivel", err)
	}
}

// TestNewQuizImageDialContextPortaBloqueada cobre a checagem de porta —
// porta baixa/privilegiada fora de 80/443 é recusada mesmo com IP público,
// sem depender de rede (a porta é validada antes do dial).
func TestNewQuizImageDialContextPortaBloqueada(t *testing.T) {
	dial := newQuizImageDialContext(nil)
	_, err := dial(context.Background(), "tcp", "8.8.8.8:22")
	if !errors.Is(err, errQuizImagemURLIndisponivel) {
		t.Errorf("dial em porta 22 = %v, queria errQuizImagemURLIndisponivel", err)
	}
}

// ── quizImageCheckRedirect — limite de redirects, sem rede ──────────────

func TestQuizImageCheckRedirect(t *testing.T) {
	mkReq := func(rawurl string) *http.Request {
		u, err := url.Parse(rawurl)
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		return &http.Request{URL: u}
	}

	// Dentro do limite (3), esquema http → permite.
	via2 := []*http.Request{mkReq("http://a.exemplo.com"), mkReq("http://b.exemplo.com")}
	if err := quizImageCheckRedirect(mkReq("http://c.exemplo.com"), via2); err != nil {
		t.Errorf("2º redirect deveria ser permitido, veio %v", err)
	}

	// No limite (3 hops anteriores) → rejeita o 4º.
	via3 := []*http.Request{mkReq("http://a.exemplo.com"), mkReq("http://b.exemplo.com"), mkReq("http://c.exemplo.com")}
	if err := quizImageCheckRedirect(mkReq("http://d.exemplo.com"), via3); !errors.Is(err, errQuizImagemURLIndisponivel) {
		t.Errorf("4º redirect deveria ser rejeitado, veio %v", err)
	}

	// Esquema não http/https no destino do redirect → rejeita mesmo dentro
	// do limite de hops.
	if err := quizImageCheckRedirect(mkReq("file:///etc/passwd"), via2); !errors.Is(err, errQuizImagemURLIndisponivel) {
		t.Errorf("redirect pra file:// deveria ser rejeitado, veio %v", err)
	}
}

// ── downloadQuizImage — validação de conteúdo, via httptest (sem o dialer de SSRF) ──

func TestDownloadQuizImageSucessoComContentTypeCorreto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngMagicBytes)
	}))
	defer srv.Close()

	b64, mime, err := downloadQuizImage(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("downloadQuizImage: %v", err)
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, queria image/png", mime)
	}
	if b64 == "" {
		t.Error("imagem em base64 veio vazia")
	}
}

func TestDownloadQuizImageSniffaContentTypeErrado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Servidor manda um Content-Type genérico, mas os bytes SÃO png —
		// detectQuizImageMime precisa identificar pelo sniffing.
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(pngMagicBytes)
	}))
	defer srv.Close()

	_, mime, err := downloadQuizImage(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("downloadQuizImage: %v", err)
	}
	if mime != "image/png" {
		t.Errorf("mime sniffado = %q, queria image/png", mime)
	}
}

// TestDownloadQuizImageConteudoNaoImagemRejeitado cobre "conteúdo que não é
// imagem → rejeitado".
func TestDownloadQuizImageConteudoNaoImagemRejeitado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>não é imagem</body></html>"))
	}))
	defer srv.Close()

	_, _, err := downloadQuizImage(context.Background(), srv.Client(), srv.URL)
	if !errors.Is(err, errQuizImagemURLNaoImagem) {
		t.Errorf("erro = %v, queria errQuizImagemURLNaoImagem", err)
	}
}

// TestDownloadQuizImageRespostaMaiorQueOTetoRejeitada cobre "resposta maior
// que o teto → rejeitada" (quizMaxImageBytes = 8MB).
func TestDownloadQuizImageRespostaMaiorQueOTetoRejeitada(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		// Corpo maior que quizMaxImageBytes (8MB) — escrito em pedaços pra
		// não alocar tudo de uma vez à toa no teste.
		chunk := make([]byte, 1<<20) // 1MB
		for i := 0; i < 9; i++ {     // 9MB > teto de 8MB
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	_, _, err := downloadQuizImage(context.Background(), srv.Client(), srv.URL)
	if !errors.Is(err, errQuizImagemURLGrandeDemais) {
		t.Errorf("erro = %v, queria errQuizImagemURLGrandeDemais", err)
	}
}

func TestDownloadQuizImageStatusDeErroRejeitado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, _, err := downloadQuizImage(context.Background(), srv.Client(), srv.URL)
	if !errors.Is(err, errQuizImagemURLIndisponivel) {
		t.Errorf("erro = %v, queria errQuizImagemURLIndisponivel", err)
	}
}

// ── esquema — sem rede, checado no texto da URL ─────────────────────────

func TestDownloadQuizImageEsquemaNaoSuportadoRejeitado(t *testing.T) {
	casos := []string{
		"file:///etc/passwd",
		"gopher://exemplo.com/1",
		"ftp://exemplo.com/imagem.png",
		"javascript:alert(1)",
	}
	for _, u := range casos {
		_, _, err := downloadQuizImage(context.Background(), http.DefaultClient, u)
		if !errors.Is(err, errQuizImagemURLEsquemaInvalido) {
			t.Errorf("downloadQuizImage(%q) = %v, queria errQuizImagemURLEsquemaInvalido", u, err)
		}
	}
}

// ── fetchQuizImageURL — ponto de entrada de produção ────────────────────

// TestFetchQuizImageURLProducaoBloqueiaIPPrivado confere que o ponto de
// entrada real (o que handlers_quiz.go conecta em quizDeps.fetchImage) usa
// o dialer protegido de verdade — não só os helpers testados acima
// isoladamente.
func TestFetchQuizImageURLProducaoBloqueiaIPPrivado(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), quizImageFetchTimeout)
	defer cancel()
	_, _, err := fetchQuizImageURL(ctx, "http://127.0.0.1:80/imagem.png")
	if !errors.Is(err, errQuizImagemURLIndisponivel) {
		t.Errorf("fetchQuizImageURL(127.0.0.1) = %v, queria errQuizImagemURLIndisponivel", err)
	}
}

// TestFetchQuizImageURLEsquemaInvalido confere a rejeição de esquema também
// pelo ponto de entrada de produção.
func TestFetchQuizImageURLEsquemaInvalido(t *testing.T) {
	_, _, err := fetchQuizImageURL(context.Background(), "file:///etc/passwd")
	if !errors.Is(err, errQuizImagemURLEsquemaInvalido) {
		t.Errorf("fetchQuizImageURL(file://) = %v, queria errQuizImagemURLEsquemaInvalido", err)
	}
}

// ── integração com answerQuiz/quizErr ───────────────────────────────────

// TestResolveQuizImageURLBaixaEPreencheBase64 confere que resolveQuizImageURL
// (quiz.go) transforma ImageURL em ImageBase64/ImageMime usando o
// deps.fetchImage injetado — sem tocar rede nenhuma aqui (fake).
func TestResolveQuizImageURLBaixaEPreencheBase64(t *testing.T) {
	deps := quizDeps{
		fetchImage: func(ctx context.Context, u string) (string, string, error) {
			if u != "https://prova.exemplo.com/figura.png" {
				t.Fatalf("URL inesperada: %s", u)
			}
			return "YmFzZTY0", "image/png", nil
		},
	}
	req := quizRequest{ImageURL: "https://prova.exemplo.com/figura.png"}
	got, err := resolveQuizImageURL(context.Background(), req, deps)
	if err != nil {
		t.Fatalf("resolveQuizImageURL: %v", err)
	}
	if got.ImageBase64 != "YmFzZTY0" || got.ImageMime != "image/png" {
		t.Errorf("req resolvida = %+v", got)
	}
}

// TestResolveQuizImageURLImageBase64PrevaleceSobreImageURL confere que
// ImageBase64 já preenchido nunca dispara o download — deps.fetchImage nem
// deveria ser chamado.
func TestResolveQuizImageURLImageBase64PrevaleceSobreImageURL(t *testing.T) {
	chamou := false
	deps := quizDeps{
		fetchImage: func(ctx context.Context, u string) (string, string, error) {
			chamou = true
			return "", "", nil
		},
	}
	req := quizRequest{ImageBase64: "jaTenho", ImageMime: "image/png", ImageURL: "https://exemplo.com/x.png"}
	got, err := resolveQuizImageURL(context.Background(), req, deps)
	if err != nil {
		t.Fatalf("resolveQuizImageURL: %v", err)
	}
	if chamou {
		t.Error("deps.fetchImage não deveria ser chamado quando ImageBase64 já veio preenchido")
	}
	if got.ImageBase64 != "jaTenho" {
		t.Errorf("ImageBase64 = %q, queria preservado", got.ImageBase64)
	}
}

// TestResolveQuizImageURLFalhaNaoReservaCota confere, no nível de
// answerQuiz, que uma falha de download NUNCA chama deps.reserve — mesma
// regra que já vale pra validateQuizImage no caminho de imageBase64 (a cota
// só é gasta depois que a questão foi validada como respondível).
func TestResolveQuizImageURLFalhaNaoReservaCota(t *testing.T) {
	reservou := false
	deps := quizDeps{
		fetchImage: func(ctx context.Context, u string) (string, string, error) {
			return "", "", errQuizImagemURLIndisponivel
		},
		reserve: func() error {
			reservou = true
			return nil
		},
	}
	req := quizRequest{Raw: "Qual a capital da França?\nA) Londres\nB) Paris", ImageURL: "http://127.0.0.1/x.png"}
	_, err := answerQuiz(context.Background(), req, deps)
	if !errors.Is(err, errQuizImagemURLIndisponivel) {
		t.Fatalf("answerQuiz erro = %v, queria errQuizImagemURLIndisponivel", err)
	}
	if reservou {
		t.Error("deps.reserve foi chamado apesar do download da imagem ter falhado")
	}
}

// TestQuizErrMapeiaErrosDeImageURL confere que quizErr traduz as quatro
// causas de falha de imageUrl pro código IMAGE_FETCH_FAILED, 400, com
// mensagens diferentes por causa (mesmo padrão de INVALID_IMAGE já usado
// pras três causas de imageBase64 inválido).
func TestQuizErrMapeiaErrosDeImageURL(t *testing.T) {
	casos := []error{
		errQuizImagemURLEsquemaInvalido,
		errQuizImagemURLIndisponivel,
		errQuizImagemURLNaoImagem,
		errQuizImagemURLGrandeDemais,
	}
	for _, e := range casos {
		ae := quizErr(e)
		if ae.Code != "IMAGE_FETCH_FAILED" || ae.Status != http.StatusBadRequest {
			t.Errorf("quizErr(%v) = %s/%d, queria IMAGE_FETCH_FAILED/400", e, ae.Code, ae.Status)
		}
		if ae.Message == "" {
			t.Errorf("quizErr(%v) sem mensagem", e)
		}
	}
	// Mensagens diferentes entre si — igual à checagem já existente pras
	// causas de INVALID_IMAGE em TestQuizErrStatus.
	msgs := map[string]bool{}
	for _, e := range casos {
		msgs[quizErr(e).Message] = true
	}
	if len(msgs) != len(casos) {
		t.Errorf("mensagens de IMAGE_FETCH_FAILED deveriam ser todas diferentes, veio %v", msgs)
	}
}
