package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// posthogClient consulta a Query API (HogQL) da PostHog pra alimentar a tela
// de Analytics sem precisar abrir o app.posthog.com. Só leitura.
type posthogClient struct {
	host      string
	projectID string
	token     string
	http      *http.Client
}

func newPostHogClient(host, projectID, token string) *posthogClient {
	return &posthogClient{
		host:      host,
		projectID: projectID,
		token:     token,
		http:      &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *posthogClient) enabled() bool { return c.projectID != "" && c.token != "" }

// hogqlRow é uma linha genérica de resultado — a ordem das colunas bate com
// a query que cada handler montou.
type hogqlRow []any

// query executa uma HogQLQuery na PostHog e devolve as linhas cruas. A query
// é sempre montada pelo código (nunca a partir de input de usuário sem passar
// por whitelist antes — ver posthogRangeDays), pra não abrir injeção HogQL.
func (c *posthogClient) query(ctx context.Context, hogql string) ([]hogqlRow, error) {
	body, err := json.Marshal(map[string]any{
		"query": map[string]string{"kind": "HogQLQuery", "query": hogql},
	})
	if err != nil {
		return nil, err
	}
	reqURL := fmt.Sprintf("%s/api/projects/%s/query/", c.host, c.projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("posthog respondeu %d", resp.StatusCode)
	}
	var out struct {
		Results []hogqlRow `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Results, nil
}
