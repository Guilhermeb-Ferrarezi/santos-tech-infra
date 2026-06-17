package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

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
	return c.doRaw(ctx, method, url, authorization, contentType, reader)
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
