package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// Política de retry para chamadas com corpo rebobinável (do/doBot): poucas
// tentativas em erros transientes — falha de rede (request nem completou) ou
// 502/503/504 do destino. Backoff exponencial com jitter para não martelar.
const maxAttempts = 3

func transient(status int, err error) bool {
	if err != nil {
		return true // request não completou (rede/timeout): seguro repetir
	}
	return status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// backoff devolve o atraso antes da tentativa attempt (1-based): ~exponencial
// (200ms, 400ms, …) com jitter de até 50%.
func backoff(attempt int) time.Duration {
	base := 200 * time.Millisecond * time.Duration(1<<(attempt-1))
	jitter := time.Duration(rand.Int63n(int64(base) / 2))
	return base + jitter
}

// retryDo repete fn enquanto o resultado for transiente, respeitando o ctx.
func retryDo(ctx context.Context, fn func() (int, []byte, error)) (int, []byte, error) {
	var status int
	var raw []byte
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		status, raw, err = fn()
		if !transient(status, err) || attempt == maxAttempts {
			return status, raw, err
		}
		select {
		case <-ctx.Done():
			return status, raw, err
		case <-time.After(backoff(attempt)):
		}
	}
	return status, raw, err
}

// apiClient chama as APIs do ecossistema repassando o Authorization do usuário.
// O MCP não valida token nem decide permissão — quem decide é a API de destino.
type apiClient struct {
	http *http.Client
}

func newAPIClient() *apiClient {
	// Teto absoluto; o prazo efetivo de cada chamada vem do contexto (proxy).
	return &apiClient{http: &http.Client{Timeout: 120 * time.Second}}
}

// do executa a chamada com corpo JSON e devolve (status, corpo). Erro só quando
// a requisição nem completou (rede, timeout); status >= 400 é decisão de quem chamou.
func (c *apiClient) do(ctx context.Context, method, url, authorization string, body any) (int, []byte, error) {
	var reader io.Reader
	contentType := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("serializando corpo: %w", err)
		}
		reader = bytes.NewReader(b)
		contentType = "application/json"
	}
	// Corpo já materializado em bytes: rebobinável, então pode repetir.
	return retryDo(ctx, func() (int, []byte, error) {
		if r, ok := reader.(*bytes.Reader); ok {
			r.Seek(0, io.SeekStart)
		}
		return c.doRaw(ctx, method, url, authorization, contentType, reader)
	})
}

// doBot chama o dashboard API do bot-go autenticando com a DASH key de serviço
// (header X-Dash-Key), e NÃO com o Authorization do usuário — o dashboard do bot
// usa uma chave estática própria, não o JWT/PAT do auth central.
func (c *apiClient) doBot(ctx context.Context, method, url, dashKey string, body any) (int, []byte, error) {
	var reader io.Reader
	contentType := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("serializando corpo: %w", err)
		}
		reader = bytes.NewReader(b)
		contentType = "application/json"
	}
	return retryDo(ctx, func() (int, []byte, error) {
		if r, ok := reader.(*bytes.Reader); ok {
			r.Seek(0, io.SeekStart)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reader)
		if err != nil {
			return 0, nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("X-Dash-Key", dashKey)
		resp, err := c.http.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4MB de teto
		if err != nil {
			return 0, nil, err
		}
		return resp.StatusCode, raw, nil
	})
}

// doRaw é a variante de baixo nível: corpo e Content-Type arbitrários
// (ex.: multipart para uploads).
func (c *apiClient) doRaw(ctx context.Context, method, url, authorization, contentType string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4MB de teto
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}
