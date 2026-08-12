package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// instagramClient envia private replies via Graph API do Instagram
// (graph.instagram.com — "Instagram API com login do Instagram", não a
// Facebook Login antiga). Validado em ambiente real: Standard Access (sem
// App Review) já autoriza POST /{ig-user-id}/messages com recipient.comment_id
// para a própria conta business — só a permissão instagram_manage_comments
// (leitura) parecia exigir escopo à parte; o envio funcionou direto.
type instagramClient struct {
	baseURL string // ex: https://graph.instagram.com/v21.0
	userID  string
	token   string
	client  *http.Client
}

func newInstagramClient(cfg Config) *instagramClient {
	return &instagramClient{
		baseURL: "https://graph.instagram.com/v21.0",
		userID:  cfg.InstagramUserID,
		token:   cfg.InstagramAccessToken,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *instagramClient) enabled() bool { return c.userID != "" && c.token != "" }

// sendPrivateReply responde por DM a um comentário específico. A Graph API só
// aceita uma private reply por comment_id (chamadas repetidas para o mesmo
// comentário falham do lado da Meta) — a deduplicação por comment_id fica a
// cargo do chamador (ver handleInstagramComment, dedupe via Redis).
func (c *instagramClient) sendPrivateReply(ctx context.Context, commentID, text string) error {
	if !c.enabled() {
		return fmt.Errorf("instagram client não configurado (INSTAGRAM_USER_ID/INSTAGRAM_ACCESS_TOKEN ausentes)")
	}
	body, err := json.Marshal(map[string]any{
		"recipient": map[string]string{"comment_id": commentID},
		"message":   map[string]string{"text": text},
	})
	if err != nil {
		return fmt.Errorf("marshal private reply: %w", err)
	}
	endpoint := fmt.Sprintf("%s/%s/messages?access_token=%s", c.baseURL, c.userID, url.QueryEscape(c.token))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("criar request de private reply: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("enviar private reply: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return fmt.Errorf("graph api retornou status %d: %q", res.StatusCode, b)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return nil
}
