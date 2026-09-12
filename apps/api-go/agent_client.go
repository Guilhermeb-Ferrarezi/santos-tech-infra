package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cliente do agent-go (POST {AGENT_URL}/claude/generate) — é por aqui que a
// API central pede texto ao Claude sem ter chave de API própria: o agent-go
// roda o CLI com a assinatura da empresa. Mesmo caminho e mesmo contrato do
// bot-go (apps/bot-go/agent_go.go): {task:"raw", brief, model} → {text}.
//
// Usado pelo Pós-aula (posaula_gerar.go) pra gerar as práticas e corrigir
// resposta aberta. Só texto: anexo binário nunca passa por aqui.

const (
	// claudeRawTimeout: 2 min por chamada. Uma geração de 4 práticas com o
	// sonnet leva bem menos que isso; o teto curto existe pra o orçamento da
	// task caber no Timeout de 10 min do asynq (posaula_gerar.go): 2 chamadas
	// (a segunda é a retentativa de JSON inválido) × 2 tentativas HTTP × 2 min
	// + as pausas ainda deixam folga pra gravar o estado final.
	claudeRawTimeout = 2 * time.Minute
	// claudeRawTentativas: 1 retentativa em erro de rede, 5xx ou 429 — o
	// agent-go pode estar reiniciando ou com o balde de 10/min cheio; os
	// outros 4xx (401 de secret errado, 400) não mudam repetindo na hora.
	claudeRawTentativas = 2
	// claudeRawRetryAfterMax: teto do Retry-After que a gente respeita antes
	// da retentativa interna — mais que isso é melhor devolver o erro
	// transitório e deixar o asynq retentar com backoff.
	claudeRawRetryAfterMax = 60 * time.Second
)

// errAgentSecretMissing é o erro (estável, sem detalhe de rede) que vai pro
// ai_error do diário quando ninguém configurou o segredo — a tela do
// professor mostra isso como está, então tem que ser legível por humano.
var errAgentSecretMissing = errors.New("AGENT_INTERNAL_SECRET não configurado")

// agentHTTPClient é compartilhado (pool de conexões) e tem o timeout longo
// de inferência — não use pra mais nada.
var agentHTTPClient = &http.Client{Timeout: claudeRawTimeout}

// claudeRawRetryDelay é a pausa antes da retentativa. Variável (não const)
// só pra o teste zerar e não dormir 3s por caso.
var claudeRawRetryDelay = 3 * time.Second

// agentTransientError embrulha a falha que ainda vale retentar MAIS TARDE
// (rede fora, 5xx ou 429 do agent-go) — o handler asynq usa isso pra decidir
// entre devolver o erro (retry com backoff) e desistir de vez (outros 4xx,
// sem secret).
type agentTransientError struct{ err error }

func (e agentTransientError) Error() string { return e.err.Error() }
func (e agentTransientError) Unwrap() error { return e.err }

type claudeGenerateRequest struct {
	Task  string `json:"task"`
	Brief string `json:"brief"`
	Model string `json:"model"`
}

type claudeGenerateResponse struct {
	Text string `json:"text"`
}

// claudeRaw manda o brief inteiro como prompt e devolve o texto cru do modelo.
// Sem secret configurado falha na hora, com erro claro (nunca panic, nunca
// chamada sem auth). Nunca loga o secret nem o brief — só o tamanho.
func (s *Server) claudeRaw(ctx context.Context, brief, model string) (string, error) {
	return s.claudeRawCom(ctx, brief, model, claudeRawTimeout)
}

// claudeRawCom é o claudeRaw com teto de tempo próprio por chamada — a
// semente do Material vivo (posaula_material.go) pede 12 mil caracteres ao
// opus, que não cabem nos 2 min do padrão. O cliente extra compartilha o
// transporte (pool de conexões) do padrão; só o Timeout muda.
func (s *Server) claudeRawCom(ctx context.Context, brief, model string, timeout time.Duration) (string, error) {
	if strings.TrimSpace(s.cfg.AgentInternalSecret) == "" {
		return "", errAgentSecretMissing
	}
	if model == "" {
		model = "sonnet"
	}
	client := agentHTTPClient
	if timeout > 0 && timeout != claudeRawTimeout {
		client = &http.Client{Timeout: timeout, Transport: agentHTTPClient.Transport}
	}
	payload, err := json.Marshal(claudeGenerateRequest{Task: "raw", Brief: brief, Model: model})
	if err != nil {
		return "", fmt.Errorf("agent: marshal: %w", err)
	}
	url := s.cfg.AgentURL + "/claude/generate"
	var lastErr error
	lastRetry := false
	espera := claudeRawRetryDelay
	for tentativa := 1; tentativa <= claudeRawTentativas; tentativa++ {
		if tentativa > 1 {
			select {
			case <-ctx.Done():
				return "", agentTransientError{ctx.Err()}
			case <-time.After(espera):
			}
		}
		text, retry, retryAfter, err := s.claudeRawOnce(ctx, client, url, payload)
		if err == nil {
			return text, nil
		}
		lastErr, lastRetry = err, retry
		slog.Warn("agent-go: chamada falhou", "tentativa", tentativa, "retry", retry, "retryAfter", retryAfter, "briefChars", len(brief), "err", err)
		if !retry {
			break
		}
		// 429 com Retry-After: o agent-go disse quando o balde libera —
		// esperar menos que isso só gasta a retentativa à toa.
		if retryAfter > 0 {
			espera = retryAfter
		}
	}
	if lastRetry {
		return "", agentTransientError{lastErr}
	}
	return "", lastErr
}

// claudeRawOnce faz UMA requisição. retry diz se vale tentar de novo (erro
// de rede, 5xx ou 429); os outros 4xx e resposta malformada são definitivos.
// retryAfter é o que o servidor pediu pra esperar (cabeçalho Retry-After em
// segundos, teto claudeRawRetryAfterMax) — zero quando não veio.
func (s *Server) claudeRawOnce(ctx context.Context, client *http.Client, url string, payload []byte) (text string, retry bool, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", false, 0, fmt.Errorf("agent: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.AgentInternalSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", true, 0, fmt.Errorf("agent: http: %w", err)
	}
	defer resp.Body.Close()
	// 1 MB de teto: uma resposta legítima tem poucos KB; mais que isso é o
	// agent-go devolvendo algo que não é a resposta.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", true, 0, fmt.Errorf("agent: ler resposta: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		// 5xx e 429 (rate limit de 10/min do agent-go, dividido por toda a
		// api-go) passam sozinhos com o tempo; 503 já é >= 500.
		retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return "", retry, retryAfterEspera(resp.Header.Get("Retry-After")), fmt.Errorf("agent: status %d: %s", resp.StatusCode, msg)
	}
	var out claudeGenerateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", false, 0, fmt.Errorf("agent: resposta não é JSON: %w", err)
	}
	if strings.TrimSpace(out.Text) == "" {
		return "", false, 0, errors.New("agent: resposta vazia")
	}
	return out.Text, false, 0, nil
}

// retryAfterEspera lê um Retry-After em segundos (o único formato que o
// agent-go manda) e aplica o teto. Ausente, vazio, não numérico ou data
// HTTP → 0, que significa "usa a pausa padrão".
func retryAfterEspera(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	segundos, err := strconv.Atoi(header)
	if err != nil || segundos <= 0 {
		return 0
	}
	d := time.Duration(segundos) * time.Second
	if d > claudeRawRetryAfterMax {
		d = claudeRawRetryAfterMax
	}
	return d
}
