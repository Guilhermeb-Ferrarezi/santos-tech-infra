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

// do executa a chamada e devolve (status, corpo). Erro só quando a requisição
// nem completou (rede, timeout); status >= 400 é decisão de quem chamou.
func (c *apiClient) do(ctx context.Context, method, url, authorization string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("serializando corpo: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
