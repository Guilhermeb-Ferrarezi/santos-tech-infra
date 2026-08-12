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

// InstagramMedia é a view simplificada de uma publicação, usada pelo seletor
// visual da tela de mapeamento (admin escolhe o post em vez de colar o ID).
type InstagramMedia struct {
	ID           string `json:"id"`
	Caption      string `json:"caption"`
	MediaType    string `json:"mediaType"`
	MediaURL     string `json:"mediaUrl"`
	ThumbnailURL string `json:"thumbnailUrl"`
	Permalink    string `json:"permalink"`
	Timestamp    string `json:"timestamp"`
}

// listRecentMedia busca as publicações mais recentes da conta conectada, pra
// popular o seletor visual (em vez do admin precisar saber o media_id de cor).
func (c *instagramClient) listRecentMedia(ctx context.Context) ([]InstagramMedia, error) {
	if !c.enabled() {
		return nil, fmt.Errorf("instagram client não configurado (INSTAGRAM_USER_ID/INSTAGRAM_ACCESS_TOKEN ausentes)")
	}
	endpoint := fmt.Sprintf("%s/%s/media?fields=id,caption,media_type,media_url,thumbnail_url,timestamp,permalink&limit=30&access_token=%s",
		c.baseURL, c.userID, url.QueryEscape(c.token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("criar request de listagem de mídia: %w", err)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listar mídia: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil, fmt.Errorf("graph api retornou status %d: %q", res.StatusCode, b)
	}
	var out struct {
		Data []struct {
			ID           string `json:"id"`
			Caption      string `json:"caption"`
			MediaType    string `json:"media_type"`
			MediaURL     string `json:"media_url"`
			ThumbnailURL string `json:"thumbnail_url"`
			Permalink    string `json:"permalink"`
			Timestamp    string `json:"timestamp"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decodificar listagem de mídia: %w", err)
	}
	media := make([]InstagramMedia, len(out.Data))
	for i, m := range out.Data {
		media[i] = InstagramMedia{
			ID: m.ID, Caption: m.Caption, MediaType: m.MediaType,
			MediaURL: m.MediaURL, ThumbnailURL: m.ThumbnailURL,
			Permalink: m.Permalink, Timestamp: m.Timestamp,
		}
	}
	return media, nil
}
