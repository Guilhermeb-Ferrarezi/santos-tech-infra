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
	body, _ := json.Marshal(map[string]string{"to": to, "subject": subject, "html": html})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", e.key)
	res, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("email api retornou status %d", res.StatusCode)
	}
	return nil
}
