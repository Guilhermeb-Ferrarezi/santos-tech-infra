package main

// Roteador de chaves de API: executa chamadas contra APIs externas
// rotacionando entre as chaves cadastradas de um provider. Quando uma chave
// responde com um dos códigos configurados como "não autorizado" (401 por
// padrão) ou "sem créditos" (402/429 por padrão), ela é marcada e a próxima
// chave ativa é tentada, até uma funcionar ou todas se esgotarem.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santos-tech/auth/db"
)

var apiRouterHTTP = &http.Client{Timeout: 30 * time.Second}

const (
	// apiRouterMaxResponseBytes limita o corpo lido de um provider. Sem teto,
	// um provider (ou quem sequestrasse a rota) podia streamar até derrubar o
	// processo por OOM: a resposta é lida INTEIRA em memória antes de voltar.
	// 32MB cobre com folga áudio/imagem em base64 (o request de op já é 25MB).
	apiRouterMaxResponseBytes = 32 << 20

	// apiRouterRotationBudget é o teto TOTAL da rotação de chaves. Cada
	// tentativa tem 30s (apiRouterHTTP.Timeout); com 10 chaves cadastradas, a
	// requisição do admin ficava presa até 300s. Agora o ctx morre em 60s.
	apiRouterRotationBudget = 60 * time.Second

	// apiRouterMaxKeyAttempts limita quantas chaves são tentadas numa mesma
	// requisição, independente do relógio.
	apiRouterMaxKeyAttempts = 4
)

// errAPIRouterResponseTooLarge: o provider devolveu mais do que
// apiRouterMaxResponseBytes.
var errAPIRouterResponseTooLarge = errors.New("apirouter: resposta do provider grande demais")

// readAPIRouterBody lê o corpo com teto. Devolve erro em vez de truncar em
// silêncio — um JSON cortado pela metade viraria "erro do adapter" mais na
// frente, escondendo a causa.
func readAPIRouterBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, apiRouterMaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > apiRouterMaxResponseBytes {
		return nil, errAPIRouterResponseTooLarge
	}
	return b, nil
}

var errAPIRouterNoActiveKeys = errors.New("apirouter: nenhuma chave ativa para o provider")
var errAPIRouterAllKeysExhausted = errors.New("apirouter: todas as chaves ativas falharam")

// errAPIRouterInvalidPath: o path pedido tentaria sair do host do provider
// (sequestro de host) — a requisição carrega a chave decifrada no header de
// auth, então mandar para outro host é exfiltração de credencial.
var errAPIRouterInvalidPath = errors.New("apirouter: path inválido (precisa começar com / e não pode trocar o host do provider)")

const (
	apiRouterOutcomeSuccess        = "success"
	apiRouterOutcomeUnauthorized   = "unauthorized"
	apiRouterOutcomeNoCredits      = "no_credits"
	apiRouterOutcomeTransportError = "transport_error"
)

// ── helpers puros (sem I/O — testáveis sem Postgres) ─────────────────────────

// classifyAPIRouterStatus decide o desfecho de uma resposta HTTP a partir dos
// códigos configurados no provider.
func classifyAPIRouterStatus(unauthorizedCodes, noCreditCodes []int32, statusCode int) string {
	if containsCode(unauthorizedCodes, statusCode) {
		return apiRouterOutcomeUnauthorized
	}
	if containsCode(noCreditCodes, statusCode) {
		return apiRouterOutcomeNoCredits
	}
	return apiRouterOutcomeSuccess
}

func containsCode(codes []int32, code int) bool {
	for _, c := range codes {
		if int(c) == code {
			return true
		}
	}
	return false
}

// authHeaderValue monta o valor do header de auth: "<scheme> <secret>" ou só
// "<secret>" quando o provider não usa prefixo (auth_scheme vazio).
func authHeaderValue(scheme, secret string) string {
	scheme = strings.TrimSpace(scheme)
	if scheme == "" {
		return secret
	}
	return scheme + " " + secret
}

// apiRouterRequestURL junta base_url + path SEM deixar o path trocar o host.
// A concatenação crua ("https://api.x.com" + path) era sequestrável: um path
// como "@evil.com/v1/models" produz "https://api.x.com@evil.com/v1/models",
// cujo host é evil.com — e buildAPIRouterRequest manda a chave decifrada no
// header de auth. Mesma armadilha com "//evil.com/x" (URL protocolo-relativa).
//
// Regras: path vazio = a própria base_url (usado por test_path vazio); senão
// precisa começar com "/" e não pode começar com "//". A query é separada
// antes do JoinPath (que escaparia o "?") e reanexada depois. No fim,
// scheme/host/userinfo do resultado são conferidos contra a base — cinto e
// suspensório caso alguma regra acima escape.
func apiRouterRequestURL(baseURL, path string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return "", fmt.Errorf("apirouter: base_url inválida: %q", baseURL)
	}
	if path == "" {
		return base.String(), nil
	}
	if !apiRouterValidPath(path) {
		return "", errAPIRouterInvalidPath
	}
	rawPath, rawQuery, hasQuery := strings.Cut(path, "?")
	u := base.JoinPath(rawPath)
	if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil {
		return "", errAPIRouterInvalidPath
	}
	if hasQuery {
		u.RawQuery = rawQuery
	}
	return u.String(), nil
}

// apiRouterValidPath valida a SINTAXE do path (sem precisar da base_url), pra
// os handlers recusarem com 400 antes de chegar no provider e pro cadastro de
// provider barrar test_path/chat_path envenenados. Vazio é válido: test_path
// vazio significa "GET na própria base_url" e chat_path vazio cai no default
// do adapter.
func apiRouterValidPath(path string) bool {
	if path == "" {
		return true
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return false
	}
	if strings.Contains(path, "#") {
		return false
	}
	return strings.IndexFunc(path, isCtlByte) < 0
}

// isCtlByte reporta se r é um caractere de controle (CR/LF/NUL/DEL...), que
// não tem uso legítimo num path e serve pra confundir proxies.
func isCtlByte(r rune) bool {
	return r < 0x20 || r == 0x7f
}

// buildAPIRouterRequest monta a requisição HTTP para uma chave decifrada:
// base_url do provider + path, com o header de auth configurado e headers extras.
// contentType vazio com body != nil assume application/json (comportamento
// padrão de chat/proxy); preencha para body binário/multipart.
func buildAPIRouterRequest(ctx context.Context, provider db.ApiRouterProvider, secret, method, path string, body []byte, contentType string, extraHeaders map[string]string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	full, err := apiRouterRequestURL(provider.BaseUrl, path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, full, reader)
	if err != nil {
		return nil, fmt.Errorf("apirouter: montar request: %w", err)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	if body != nil {
		// Sem isso o Go envia body sem Content-Type e providers estritos
		// (Mistral, Together, DeepSeek...) rejeitam com 400/415.
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set(provider.AuthHeader, authHeaderValue(provider.AuthScheme, secret))
	return req, nil
}

// ── execução (toca Postgres via s.q — não coberto por teste unitário; o
// repo não roda Postgres real no CI, ver server_test.go/handlers_boards_test.go) ──

// apiRouterAttempt registra o desfecho de uma tentativa com uma chave específica.
type apiRouterAttempt struct {
	KeyID      int64  `json:"keyId"`
	KeyLabel   string `json:"keyLabel"`
	StatusCode int    `json:"statusCode"`
	Outcome    string `json:"outcome"`
}

// apiRouterOutcome é o resultado final de Execute: a resposta que "venceu" a
// rotação, mais o histórico de tentativas.
type apiRouterOutcome struct {
	StatusCode int
	Body       []byte
	KeyID      int64
	KeyLabel   string
	Attempts   []apiRouterAttempt
}

// decryptAPIRouterKeySecret decifra o segredo de uma chave do roteador. Aceita
// tanto o formato v2 (HKDF + AAD amarrado ao provider/tail) quanto o legado
// v1 que já está gravado no banco — ver vault.go.
func (s *Server) decryptAPIRouterKeySecret(ctx context.Context, k db.ApiRouterKey) (string, error) {
	secret, _, err := s.vault.DecryptKeySecret(k.SecretEnc, k.ProviderID, k.SecretTail)
	return secret, err
}

// executeAPIRouterRequest tenta a requisição com cada chave ativa do provider,
// em ordem de prioridade, até uma responder fora dos códigos de
// unauthorized/no-credits. Erro de rede com uma chave não a penaliza (o
// problema não é dela) — tenta a próxima.
func (s *Server) executeAPIRouterRequest(ctx context.Context, provider db.ApiRouterProvider, method, path string, body []byte, contentType string, extraHeaders map[string]string) (*apiRouterOutcome, error) {
	keys, err := s.q.ListActiveAPIRouterKeys(ctx, provider.ID)
	if err != nil {
		return nil, fmt.Errorf("apirouter: listar chaves ativas: %w", err)
	}
	if len(keys) == 0 {
		return nil, errAPIRouterNoActiveKeys
	}

	// Teto TOTAL da rotação: sem ele, N chaves × 30s prendiam a requisição do
	// admin por minutos (10 chaves = 300s). Duas travas independentes, porque
	// falha rápida (connection refused) não gasta relógio: o ctx com deadline e
	// o número de chaves tentadas.
	ctx, cancel := context.WithTimeout(ctx, apiRouterRotationBudget)
	defer cancel()

	var attempts []apiRouterAttempt
	for i, key := range keys {
		if i >= apiRouterMaxKeyAttempts || ctx.Err() != nil {
			break
		}
		secret, err := s.decryptAPIRouterKeySecret(ctx, key)
		if err != nil {
			attempts = append(attempts, apiRouterAttempt{KeyID: key.ID, KeyLabel: key.Label, Outcome: apiRouterOutcomeTransportError})
			continue
		}
		req, err := buildAPIRouterRequest(ctx, provider, secret, method, path, body, contentType, extraHeaders)
		if err != nil {
			return nil, err
		}
		resp, err := apiRouterHTTP.Do(req)
		if err != nil {
			attempts = append(attempts, apiRouterAttempt{KeyID: key.ID, KeyLabel: key.Label, Outcome: apiRouterOutcomeTransportError})
			continue
		}
		respBody, readErr := readAPIRouterBody(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			attempts = append(attempts, apiRouterAttempt{KeyID: key.ID, KeyLabel: key.Label, StatusCode: resp.StatusCode, Outcome: apiRouterOutcomeTransportError})
			continue
		}

		outcome := classifyAPIRouterStatus(provider.UnauthorizedCodes, provider.NoCreditCodes, resp.StatusCode)
		attempts = append(attempts, apiRouterAttempt{KeyID: key.ID, KeyLabel: key.Label, StatusCode: resp.StatusCode, Outcome: outcome})

		switch outcome {
		case apiRouterOutcomeUnauthorized:
			_ = s.q.RecordAPIRouterKeyFailure(ctx, db.RecordAPIRouterKeyFailureParams{ID: key.ID, Status: "unauthorized", LastErrorCode: validInt4(resp.StatusCode)})
			continue
		case apiRouterOutcomeNoCredits:
			_ = s.q.RecordAPIRouterKeyFailure(ctx, db.RecordAPIRouterKeyFailureParams{ID: key.ID, Status: "no_credits", LastErrorCode: validInt4(resp.StatusCode)})
			continue
		default:
			_ = s.q.RecordAPIRouterKeySuccess(ctx, key.ID)
			return &apiRouterOutcome{StatusCode: resp.StatusCode, Body: respBody, KeyID: key.ID, KeyLabel: key.Label, Attempts: attempts}, nil
		}
	}
	return nil, errAPIRouterAllKeysExhausted
}

// executeAPIRouterRequestOnce executa a requisição com UMA chave específica
// (sem rotação) — usado pelo polling de operações assíncronas (AssemblyAI,
// Replicate), que precisa continuar com a MESMA chave que criou o job.
func (s *Server) executeAPIRouterRequestOnce(ctx context.Context, provider db.ApiRouterProvider, keyID int64, method, path string, body []byte, contentType string, extraHeaders map[string]string) (*apiRouterOutcome, error) {
	key, err := s.q.GetAPIRouterKey(ctx, db.GetAPIRouterKeyParams{ID: keyID, ProviderID: provider.ID})
	if err != nil {
		return nil, fmt.Errorf("apirouter: buscar chave do job: %w", err)
	}
	secret, err := s.decryptAPIRouterKeySecret(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("apirouter: decifrar chave do job: %w", err)
	}
	req, err := buildAPIRouterRequest(ctx, provider, secret, method, path, body, contentType, extraHeaders)
	if err != nil {
		return nil, err
	}
	resp, err := apiRouterHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	respBody, readErr := readAPIRouterBody(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	outcome := classifyAPIRouterStatus(provider.UnauthorizedCodes, provider.NoCreditCodes, resp.StatusCode)
	switch outcome {
	case apiRouterOutcomeUnauthorized:
		_ = s.q.RecordAPIRouterKeyFailure(ctx, db.RecordAPIRouterKeyFailureParams{ID: key.ID, Status: "unauthorized", LastErrorCode: validInt4(resp.StatusCode)})
	case apiRouterOutcomeNoCredits:
		_ = s.q.RecordAPIRouterKeyFailure(ctx, db.RecordAPIRouterKeyFailureParams{ID: key.ID, Status: "no_credits", LastErrorCode: validInt4(resp.StatusCode)})
	default:
		_ = s.q.RecordAPIRouterKeySuccess(ctx, key.ID)
	}
	return &apiRouterOutcome{StatusCode: resp.StatusCode, Body: respBody, KeyID: key.ID, KeyLabel: key.Label}, nil
}

// apiRouterTestResult é o desfecho de um teste manual (botão "testar" no
// admin) contra UMA chave específica — diferente de Execute, tenta mesmo que
// a chave não esteja "active" (é assim que o admin reativa uma chave).
type apiRouterTestResult struct {
	StatusCode int
	Outcome    string
	Error      string
}

// testAPIRouterKey dispara uma requisição de teste (provider.TestPath/
// TestMethod, ou GET na base_url se ambos vazios) usando uma chave
// específica, e já grava o resultado.
func (s *Server) testAPIRouterKey(ctx context.Context, provider db.ApiRouterProvider, key db.ApiRouterKey) (apiRouterTestResult, error) {
	secret, err := s.decryptAPIRouterKeySecret(ctx, key)
	if err != nil {
		return apiRouterTestResult{}, fmt.Errorf("apirouter: decifrar chave: %w", err)
	}

	method := provider.TestMethod
	if method == "" {
		method = http.MethodGet
	}
	req, err := buildAPIRouterRequest(ctx, provider, secret, method, provider.TestPath, nil, "", nil)
	if err != nil {
		return apiRouterTestResult{}, err
	}

	resp, err := apiRouterHTTP.Do(req)
	if err != nil {
		return apiRouterTestResult{Outcome: apiRouterOutcomeTransportError, Error: err.Error()}, nil
	}
	defer resp.Body.Close()
	// Só interessa o status: drena o começo pra reaproveitar a conexão, com teto
	// (io.Copy sem limite deixaria um provider hostil nos alimentando à vontade).
	_, _ = io.CopyN(io.Discard, resp.Body, 32<<10)

	outcome := classifyAPIRouterStatus(provider.UnauthorizedCodes, provider.NoCreditCodes, resp.StatusCode)
	switch outcome {
	case apiRouterOutcomeUnauthorized:
		_ = s.q.RecordAPIRouterKeyFailure(ctx, db.RecordAPIRouterKeyFailureParams{ID: key.ID, Status: "unauthorized", LastErrorCode: validInt4(resp.StatusCode)})
	case apiRouterOutcomeNoCredits:
		_ = s.q.RecordAPIRouterKeyFailure(ctx, db.RecordAPIRouterKeyFailureParams{ID: key.ID, Status: "no_credits", LastErrorCode: validInt4(resp.StatusCode)})
	default:
		_ = s.q.RecordAPIRouterKeySuccess(ctx, key.ID)
	}
	return apiRouterTestResult{StatusCode: resp.StatusCode, Outcome: outcome}, nil
}

func validInt4(n int) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(n), Valid: true}
}
