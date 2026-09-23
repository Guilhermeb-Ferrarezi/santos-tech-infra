package main

// Baixa a imagem referenciada por `imageUrl` quando o bookmarklet não
// consegue ler os bytes da figura na página (CORS/CSP do site da prova
// impedem canvas/fetch) — o servidor baixa em vez do navegador e segue o
// mesmo fluxo de imagem que imageBase64/imageMime já usam (ver quiz.go).
//
// Isso abre uma superfície de SSRF clássica: o usuário controla a URL, e é o
// SERVIDOR quem faz a requisição, de dentro da rede — sem defesa, daria pra
// usar a rota como proxy pra sondar serviços internos (Postgres, Redis,
// metadados de nuvem) que não deveriam ser alcançáveis de fora. As defesas
// abaixo, em ordem de importância:
//
//  1. Só http/https (rejeita file://, gopher://, etc. — ver downloadQuizImage).
//  2. O IP é validado NO MOMENTO DA CONEXÃO, não antes — ver quizImageDialContext.
//     Resolver o host e checar o resultado ANTES de discar (padrão comum, mas
//     frágil) deixaria uma janela: um resolvedor malicioso pode devolver um IP
//     público na hora da checagem e um IP privado segundos depois, na hora de
//     discar de verdade (DNS rebinding). Aqui não existe essa janela: resolve
//     UMA vez, valida TODOS os IPs devolvidos, e disca o IP JÁ VALIDADO (nunca
//     o hostname de novo) — não há uma segunda resolução pra um ataque de
//     rebinding explorar.
//  3. Redirecionamentos passam pela MESMA validação (mesmo Transport/Dialer
//     pra toda a cadeia) e param em 3 hops.
//  4. Teto de 8MB via io.LimitReader, timeout total de 10s.
//  5. Content-Type da resposta validado contra a allowlist de imagem; se
//     vier errado/ausente, tenta detectar pelos bytes e rejeita se não bater.
//  6. Nenhum cookie/header do usuário chega no destino — a requisição é nova,
//     sem nada copiado da requisição original a /quiz/answer.

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// quizImageFetchTimeout: orçamento total do download (conexão + leitura do
	// corpo) — curto e ANTES do orçamento de resposta do quiz
	// (quizTotalBudget/quizTotalBudgetImagem): baixar a imagem é uma etapa que
	// acontece antes mesmo da cota ser reservada (ver answerQuiz), não deveria
	// consumir o tempo que sobra pro modelo responder.
	quizImageFetchTimeout = 10 * time.Second
	// quizImageFetchDialTimeout: teto de CADA tentativa de conexão TCP —
	// menor que o total pra um host lento não consumir o orçamento inteiro
	// numa única tentativa.
	quizImageFetchDialTimeout = 5 * time.Second
	// quizImageFetchMaxRedirects: cada hop passa pela mesma validação de IP
	// (mesmo Transport pra toda a cadeia) — o teto existe só pra não seguir
	// uma cadeia longa/infinita.
	quizImageFetchMaxRedirects = 3
)

var (
	// errQuizImagemURLEsquemaInvalido: só http/https — checado no texto da
	// URL, antes de qualquer rede. Seguro revelar a causa: é sobre o que o
	// PRÓPRIO usuário mandou, não sobre a rede interna.
	errQuizImagemURLEsquemaInvalido = errors.New("quiz: imageUrl com esquema não suportado (use http ou https)")
	// errQuizImagemURLIndisponivel: QUALQUER falha de rede — IP bloqueado,
	// DNS que não resolve, timeout, status HTTP de erro, redirecionamentos
	// demais. Deliberadamente UMA mensagem genérica pras causas de rede: dar
	// mensagens diferentes pra "bloqueado por ser privado" vs "genuinamente
	// fora do ar" transformaria a rota num oráculo pra mapear a rede interna
	// por tentativa e erro.
	errQuizImagemURLIndisponivel = errors.New("quiz: não foi possível baixar a imagem indicada")
	// errQuizImagemURLNaoImagem/errQuizImagemURLGrandeDemais: causas
	// posteriores ao download ter FUNCIONADO — não revelam nada sobre
	// topologia de rede (o destino respondeu; só o conteúdo não serve),
	// então tudo bem serem mensagens específicas.
	errQuizImagemURLNaoImagem    = errors.New("quiz: conteúdo de imageUrl não é uma imagem suportada")
	errQuizImagemURLGrandeDemais = errors.New("quiz: imagem de imageUrl maior que o limite")
)

// quizBlockedIPRanges: faixas que a checagem de SSRF precisa recusar —
// privada, loopback, link-local, CGNAT, e as faixas de metadados de nuvem
// citadas na spec. Checadas explicitamente por CIDR em vez de confiar só nos
// métodos prontos de net.IP (IsPrivate/IsLoopback não cobrem CGNAT
// 100.64.0.0/10, por exemplo, usado por alguns provedores de metadados além
// do 169.254.169.254 padrão).
var quizBlockedIPRanges = mustParseQuizCIDRs([]string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"::1/128",
	"fc00::/7", // cobre fd00::/8 (citado explicitamente na spec)
	"fe80::/10",
})

func mustParseQuizCIDRs(cidrs []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("quiz: CIDR inválido em quizBlockedIPRanges: " + c)
		}
		nets = append(nets, n)
	}
	return nets
}

// isBlockedQuizImageIP reporta se ip não pode ser alcançado pelo download de
// imageUrl: privado, loopback, link-local, multicast, não especificado, ou
// de metadados de nuvem — a checagem que fecha SSRF, aplicada NO MOMENTO DA
// CONEXÃO (ver quizImageDialContext), nunca só antes.
func isBlockedQuizImageIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range quizBlockedIPRanges {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// isAllowedQuizImagePort: 80/443 (o caso comum) e qualquer porta >= 1024
// ("portas altas comuns de CDN", conforme a spec) — bloqueia a faixa de
// portas baixas/privilegiadas (1-1023: SSH, SMTP, DNS, e boa parte dos
// serviços administrativos internos) sem restringir CDNs que servem imagem
// em porta alta não padrão. A defesa de verdade contra alcançar um serviço
// interno é o IP (isBlockedQuizImageIP) — a porta é só profundidade extra.
func isAllowedQuizImagePort(port int) bool {
	return port == 80 || port == 443 || port >= 1024
}

// quizImageResolveFunc resolve um hostname em IPs — injetável nos testes
// (evita depender de DNS real pra simular "host que resolve pra IP privado").
// Produção usa defaultQuizImageResolve (net.DefaultResolver).
type quizImageResolveFunc func(ctx context.Context, host string) ([]net.IP, error)

func defaultQuizImageResolve(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, len(addrs))
	for i, a := range addrs {
		ips[i] = a.IP
	}
	return ips, nil
}

// newQuizImageDialContext monta o DialContext usado pelo *http.Transport do
// client de download: resolve o host UMA VEZ (via resolve, ou reconhece um
// IP já literal na própria URL), valida TODOS os IPs devolvidos — rejeita se
// QUALQUER UM deles cair numa faixa bloqueada, não só o primeiro — e só
// então disca, usando o IP LITERAL já validado (nunca o hostname de novo).
//
// É essa ausência de uma segunda resolução que fecha o ataque de DNS
// rebinding: não existe um "connect por nome" depois da checagem pra um
// resolvedor malicioso explorar devolvendo respostas diferentes em momentos
// diferentes — o connect() usa exatamente o IP que acabou de ser validado.
// Cobre redirecionamento de graça: o *http.Client reusa o MESMO Transport
// (logo o mesmo DialContext) pra cada hop, então o destino de um redirect
// passa pela validação igual à requisição original.
func newQuizImageDialContext(resolve quizImageResolveFunc) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if resolve == nil {
		resolve = defaultQuizImageResolve
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, errQuizImagemURLIndisponivel
		}
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, errQuizImagemURLIndisponivel
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || !isAllowedQuizImagePort(port) {
			return nil, errQuizImagemURLIndisponivel
		}

		var ips []net.IP
		if literal := net.ParseIP(host); literal != nil {
			ips = []net.IP{literal}
		} else {
			resolveCtx, cancel := context.WithTimeout(ctx, quizImageFetchDialTimeout)
			defer cancel()
			ips, err = resolve(resolveCtx, host)
			if err != nil || len(ips) == 0 {
				return nil, errQuizImagemURLIndisponivel
			}
		}
		for _, ip := range ips {
			if isBlockedQuizImageIP(ip) {
				return nil, errQuizImagemURLIndisponivel
			}
		}

		dialer := &net.Dialer{Timeout: quizImageFetchDialTimeout}
		target := net.JoinHostPort(ips[0].String(), portStr)
		conn, err := dialer.DialContext(ctx, network, target)
		if err != nil {
			return nil, errQuizImagemURLIndisponivel
		}
		return conn, nil
	}
}

// newQuizImageHTTPClient monta o client HTTP usado só pra baixar imageUrl —
// nunca reaproveitado pra outra coisa. resolve nil = resolvedor real
// (produção); testes injetam um fake pra não depender de DNS/rede.
func newQuizImageHTTPClient(resolve quizImageResolveFunc) *http.Client {
	transport := &http.Transport{
		DialContext: newQuizImageDialContext(resolve),
		// Proxy fica no zero-value (nil) DE PROPÓSITO — não herda
		// HTTP_PROXY/HTTPS_PROXY do processo (http.DefaultTransport herda via
		// http.ProxyFromEnvironment). Um proxy do ambiente resolveria e
		// discaria por fora do nosso DialContext, contornando a checagem de
		// IP inteira.
		TLSHandshakeTimeout:   quizImageFetchDialTimeout,
		ResponseHeaderTimeout: quizImageFetchDialTimeout,
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       quizImageFetchTimeout,
		CheckRedirect: quizImageCheckRedirect,
	}
}

// quizImageCheckRedirect limita a cadeia de redirecionamentos a
// quizImageFetchMaxRedirects e recusa um destino de esquema não http/https —
// função nomeada (não closure) pra ser testável direto, sem precisar
// exercitar rede nenhuma (ver TestQuizImageCheckRedirect). O destino em si
// (IP bloqueado ou não) é validado pelo MESMO DialContext da requisição
// original quando o *http.Client segue o redirect — não precisa de checagem
// própria aqui.
func quizImageCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= quizImageFetchMaxRedirects {
		return errQuizImagemURLIndisponivel
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return errQuizImagemURLIndisponivel
	}
	return nil
}

// downloadQuizImage baixa rawURL usando client (já com as defesas de SSRF
// aplicadas via seu Transport — ver newQuizImageHTTPClient) e devolve o
// corpo em base64 + o mime detectado, prontos pra entrar no mesmo caminho de
// imagem que imageBase64/imageMime já usam. Separado de fetchQuizImageURL
// (que monta o client de produção) só pra os testes de validação de
// conteúdo (tamanho, Content-Type) poderem injetar um *http.Client comum
// apontando pra um httptest.Server, sem precisar exercitar o dialer de SSRF
// — esse é testado à parte (ver quiz_image_fetch_test.go).
func downloadQuizImage(ctx context.Context, client *http.Client, rawURL string) (imageB64, mime string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", errQuizImagemURLEsquemaInvalido
	}

	ctx, cancel := context.WithTimeout(ctx, quizImageFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", errQuizImagemURLIndisponivel
	}
	// Sem cookie, sem Authorization, sem header nenhum copiado da requisição
	// original a /quiz/answer — nada da sessão de quem chamou a rota chega
	// no destino.
	req.Header.Set("User-Agent", "santos-tech-quiz-image-fetch/1.0")
	req.Header.Set("Accept", "image/png, image/jpeg, image/webp, image/gif")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", errQuizImagemURLIndisponivel
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", errQuizImagemURLIndisponivel
	}

	// io.LimitReader lê no máximo quizMaxImageBytes+1 — o "+1" é só pra
	// distinguir "exatamente o teto" (permitido) de "passou do teto"
	// (rejeitado) sem precisar de um segundo Read.
	limited := io.LimitReader(resp.Body, quizMaxImageBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", "", errQuizImagemURLIndisponivel
	}
	if len(body) > quizMaxImageBytes {
		return "", "", errQuizImagemURLGrandeDemais
	}
	if len(body) == 0 {
		return "", "", errQuizImagemURLIndisponivel
	}

	detected := detectQuizImageMime(resp.Header.Get("Content-Type"), body)
	if detected == "" {
		return "", "", errQuizImagemURLNaoImagem
	}

	return base64.StdEncoding.EncodeToString(body), detected, nil
}

// detectQuizImageMime confere o Content-Type declarado contra a allowlist de
// imagem (quizImagemMimesAceitos, quiz.go); se ele não bater (ausente,
// genérico como application/octet-stream, ou simplesmente errado), tenta
// identificar pelos bytes com http.DetectContentType — mesmo mecanismo de
// sniffing que o navegador usa, aqui pra VALIDAR, não pra decidir como
// renderizar. Devolve "" quando nenhuma das duas fontes aponta pra um mime
// suportado.
func detectQuizImageMime(declared string, body []byte) string {
	if ct := normalizeQuizMime(declared); quizImagemMimesAceitos[ct] {
		return ct
	}
	if ct := normalizeQuizMime(http.DetectContentType(body)); quizImagemMimesAceitos[ct] {
		return ct
	}
	return ""
}

func normalizeQuizMime(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// fetchQuizImageURL é o ponto de entrada de produção (wired em
// quizDeps.fetchImage, ver handlers_quiz.go) — monta o client com o
// resolvedor real e baixa a imagem. resolve = nil aqui sempre, em produção;
// os testes chamam downloadQuizImage/newQuizImageHTTPClient diretamente com
// um resolvedor fake em vez de passar por esta função.
func fetchQuizImageURL(ctx context.Context, rawURL string) (imageB64, mime string, err error) {
	return downloadQuizImage(ctx, newQuizImageHTTPClient(nil), rawURL)
}
