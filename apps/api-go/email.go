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

// emailClient envia emails pela NOSSA API de emails (email-sender), não pelo Resend.
type emailClient struct {
	url    string // ex: https://mails.santos-tech.com/api
	key    string
	client *http.Client
}

func newEmailClient(cfg Config) *emailClient {
	return &emailClient{
		url:    cfg.EmailAPIURL,
		key:    cfg.EmailAPIKey,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (e *emailClient) send(ctx context.Context, to, subject, html string) error {
	body, err := json.Marshal(map[string]string{"to": to, "subject": subject, "html": html})
	if err != nil {
		return fmt.Errorf("marshal email request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/send", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("criar request de email: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", e.key)
	res, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("enviar email: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("email api retornou status %d: %q", res.StatusCode, b)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return nil
}
