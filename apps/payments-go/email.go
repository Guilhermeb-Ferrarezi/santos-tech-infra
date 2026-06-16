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

// emailClient envia pela NOSSA API de emails (email-sender), não pelo Resend. Espelha o api-go.
type emailClient struct {
	url    string
	key    string
	client *http.Client
}

func newEmailClient(cfg Config) *emailClient {
	return &emailClient{url: cfg.EmailAPIURL, key: cfg.EmailAPIKey, client: &http.Client{Timeout: 20 * time.Second}}
}

func (e *emailClient) send(ctx context.Context, to, subject, html string) error {
	if e.url == "" || e.key == "" || to == "" {
		return nil // email desabilitado ou sem destinatário: não bloqueia a cobrança
	}
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
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("email api status %d: %q", res.StatusCode, b)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return nil
}

func pixEmailHTML(studentName string, amountCents int64, brCode string) string {
	reais := float64(amountCents) / 100
	return fmt.Sprintf(`<p>Olá, %s!</p><p>Sua cobrança Pix de <b>R$ %.2f</b> está disponível.</p>
<p>Copie e cole o código abaixo no seu app do banco:</p>
<pre style="background:#F5F8FA;padding:12px;border-radius:8px;word-break:break-all">%s</pre>
<p>Equipe Santos Tech</p>`, studentName, reais, brCode)
}
