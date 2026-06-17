package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type EvolutionClient struct {
	url, apiKey, instance string
	http                  *http.Client
}

func NewEvolutionClient(url, apiKey, instance string) *EvolutionClient {
	return &EvolutionClient{
		url:      strings.TrimRight(url, "/"),
		apiKey:   apiKey,
		instance: instance,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

func normalizeTo(to string) string {
	if strings.HasSuffix(to, "@g.us") {
		return to
	}
	n := strings.TrimSuffix(to, "@s.whatsapp.net")
	if i := strings.IndexAny(n, "@:"); i > 0 {
		n = n[:i]
	}
	return n
}

func (c *EvolutionClient) post(ctx context.Context, path string, body any) ([]byte, error) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/%s/%s", c.url, strings.Trim(path, "/"), c.instance),
		bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("evolution %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("evolution %s status %d: %s", path, resp.StatusCode, string(raw))
	}
	return raw, nil
}

// SendText envia texto; quotedID != "" faz a mensagem ser um reply à msg original.
// Retorna o id da nova mensagem.
func (c *EvolutionClient) SendText(ctx context.Context, to, text, quotedID string) (string, error) {
	body := map[string]any{"number": normalizeTo(to), "text": text}
	if quotedID != "" {
		body["quoted"] = map[string]any{
			"key": map[string]any{"remoteJid": to, "fromMe": true, "id": quotedID},
		}
	}
	raw, err := c.post(ctx, "message/sendText", body)
	if err != nil {
		return "", err
	}
	var out struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Key.ID, nil
}

// SendReaction reage a uma mensagem nossa no grupo (emoji "" remove a reação).
func (c *EvolutionClient) SendReaction(ctx context.Context, to, msgID, emoji string) error {
	body := map[string]any{
		"key":      map[string]any{"remoteJid": to, "fromMe": true, "id": msgID},
		"reaction": emoji,
	}
	_, err := c.post(ctx, "message/sendReaction", body)
	return err
}
