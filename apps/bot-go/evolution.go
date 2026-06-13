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

// EvolutionClient fala com a Evolution API (número não-oficial via Baileys).
// Implementa ChatSender para o engine responder por esse canal, e expõe a
// listagem de instâncias (status dos números conectados).
type EvolutionClient struct {
	url      string
	apiKey   string
	instance string
	http     *http.Client
}

// NewEvolutionClient cria o cliente. Enabled()=false se faltar url/apiKey/instance.
func NewEvolutionClient(url, apiKey, instance string) *EvolutionClient {
	return &EvolutionClient{
		url:      strings.TrimRight(url, "/"),
		apiKey:   apiKey,
		instance: instance,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *EvolutionClient) Enabled() bool {
	return c != nil && c.url != "" && c.apiKey != "" && c.instance != ""
}

// ── ChatSender ────────────────────────────────────────────────────────────────

// SendMessage envia o texto da mensagem via Evolution e retorna o id da mensagem.
func (c *EvolutionClient) SendMessage(ctx context.Context, msg OutboundMessage) (string, error) {
	return c.sendText(ctx, msg.To, msg.Content.Text)
}

// SendText envia um texto avulso para um número via Evolution.
func (c *EvolutionClient) SendText(ctx context.Context, to, text string) error {
	_, err := c.sendText(ctx, to, text)
	return err
}

// SendTypingIndicator: no-op no canal Evolution (sem indicador "digitando").
func (c *EvolutionClient) SendTypingIndicator(ctx context.Context, messageID string) error {
	return nil
}

func (c *EvolutionClient) sendText(ctx context.Context, to, text string) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("evolution: não configurado")
	}
	number := strings.TrimSuffix(to, "@s.whatsapp.net")
	if i := strings.IndexAny(number, "@:"); i > 0 {
		number = number[:i]
	}

	body := map[string]any{"number": number, "text": text}
	payload, _ := json.Marshal(body)

	url := fmt.Sprintf("%s/message/sendText/%s", c.url, c.instance)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("evolution: send http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("evolution: send status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Key.ID, nil
}

// ── Status das instâncias ─────────────────────────────────────────────────────

// EvolutionInstance é o resumo de um número conectado para o dashboard.
type EvolutionInstance struct {
	Name   string `json:"name"`
	Number string `json:"number"`
	Status string `json:"status"` // open | connecting | close
}

// Instances lista as instâncias da Evolution (status dos números conectados).
func (c *EvolutionClient) Instances(ctx context.Context) ([]EvolutionInstance, error) {
	if c == nil || c.url == "" || c.apiKey == "" {
		return nil, fmt.Errorf("evolution: não configurado")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/instance/fetchInstances", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("evolution: fetchInstances http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("evolution: fetchInstances status %d", resp.StatusCode)
	}

	// A resposta pode ser uma lista de objetos {instance:{...}} ou {name,...}.
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("evolution: fetchInstances parse: %w", err)
	}

	out := make([]EvolutionInstance, 0, len(rows))
	for _, r := range rows {
		obj := r
		if inner, ok := r["instance"]; ok {
			_ = json.Unmarshal(inner, &obj)
		}
		get := func(k string) string {
			var s string
			if v, ok := obj[k]; ok {
				_ = json.Unmarshal(v, &s)
			}
			return s
		}
		name := firstNonEmpty(get("instanceName"), get("name"))
		status := firstNonEmpty(get("connectionStatus"), get("status"), get("state"))
		number := get("ownerJid")
		if i := strings.IndexAny(number, "@:"); i > 0 {
			number = number[:i]
		}
		out = append(out, EvolutionInstance{Name: name, Number: number, Status: status})
	}
	return out, nil
}
