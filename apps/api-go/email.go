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

// provisionMailbox cria a caixa @santos-tech.com do usuário no mailserver
// (POST /mailbox/provision no email-sender) — best-effort, chamado pelo
// caller ao criar/atualizar um usuário; nunca bloqueia a operação de auth.
func (e *emailClient) provisionMailbox(ctx context.Context, email string) error {
	body, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		return fmt.Errorf("marshal provision request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/mailbox/provision", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("criar request de provisionamento: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", e.key)
	res, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("provisionar caixa: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return fmt.Errorf("email api (provision) retornou status %d: %q", res.StatusCode, b)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return nil
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
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return fmt.Errorf("email api retornou status %d: %q", res.StatusCode, b)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return nil
}
