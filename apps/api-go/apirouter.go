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
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santos-tech/auth/db"
)

var apiRouterHTTP = &http.Client{Timeout: 30 * time.Second}

var errAPIRouterNoActiveKeys = errors.New("apirouter: nenhuma chave ativa para o provider")
var errAPIRouterAllKeysExhausted = errors.New("apirouter: todas as chaves ativas falharam")

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

// buildAPIRouterRequest monta a requisição HTTP para uma chave decifrada:
// base_url do provider + path, com o header de auth configurado e headers extras.
func buildAPIRouterRequest(ctx context.Context, provider db.ApiRouterProvider, secret, method, path string, body []byte, extraHeaders map[string]string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, provider.BaseUrl+path, reader)
	if err != nil {
		return nil, fmt.Errorf("apirouter: montar request: %w", err)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	if body != nil {
		// Sem isso o Go envia body sem Content-Type e providers estritos
		// (Mistral, Together, DeepSeek...) rejeitam com 400/415.
		req.Header.Set("Content-Type", "application/json")
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

// executeAPIRouterRequest tenta a requisição com cada chave ativa do provider,
// em ordem de prioridade, até uma responder fora dos códigos de
// unauthorized/no-credits. Erro de rede com uma chave não a penaliza (o
// problema não é dela) — tenta a próxima.
func (s *Server) executeAPIRouterRequest(ctx context.Context, provider db.ApiRouterProvider, method, path string, body []byte, extraHeaders map[string]string) (*apiRouterOutcome, error) {
	keys, err := s.q.ListActiveAPIRouterKeys(ctx, provider.ID)
	if err != nil {
		return nil, fmt.Errorf("apirouter: listar chaves ativas: %w", err)
	}
	if len(keys) == 0 {
		return nil, errAPIRouterNoActiveKeys
	}

	var attempts []apiRouterAttempt
	for _, key := range keys {
		secret, err := s.vault.Decrypt(key.SecretEnc)
		if err != nil {
			attempts = append(attempts, apiRouterAttempt{KeyID: key.ID, KeyLabel: key.Label, Outcome: apiRouterOutcomeTransportError})
			continue
		}
		req, err := buildAPIRouterRequest(ctx, provider, secret, method, path, body, extraHeaders)
		if err != nil {
			return nil, err
		}
		resp, err := apiRouterHTTP.Do(req)
		if err != nil {
			attempts = append(attempts, apiRouterAttempt{KeyID: key.ID, KeyLabel: key.Label, Outcome: apiRouterOutcomeTransportError})
			continue
		}
		respBody, readErr := io.ReadAll(resp.Body)
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
	secret, err := s.vault.Decrypt(key.SecretEnc)
	if err != nil {
		return apiRouterTestResult{}, fmt.Errorf("apirouter: decifrar chave: %w", err)
	}

	method := provider.TestMethod
	if method == "" {
		method = http.MethodGet
	}
	req, err := buildAPIRouterRequest(ctx, provider, secret, method, provider.TestPath, nil, nil)
	if err != nil {
		return apiRouterTestResult{}, err
	}

	resp, err := apiRouterHTTP.Do(req)
	if err != nil {
		return apiRouterTestResult{Outcome: apiRouterOutcomeTransportError, Error: err.Error()}, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

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
