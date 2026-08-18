package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// facebookClient publica conteúdo na Página oficial via Graph API
// (graph.facebook.com — Página, não Login do Instagram). O token é um Page
// Access Token de longa duração (ver docs de setup em social_publish.go):
// derivado de um User Token de longa duração via /me/accounts, não expira
// sozinho (só se o app/permissão for revogado).
type facebookClient struct {
	baseURL string // ex: https://graph.facebook.com/v21.0
	pageID  string
	token   string
	client  *http.Client
}

func newFacebookClient(cfg Config) *facebookClient {
	return &facebookClient{
		baseURL: "https://graph.facebook.com/v21.0",
		pageID:  cfg.FacebookPageID,
		token:   cfg.FacebookAccessToken,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *facebookClient) enabled() bool { return c.pageID != "" && c.token != "" }

// publishMedia publica uma foto ou um vídeo na Página a partir de uma URL
// pública (a Graph API busca o arquivo sozinha — não aceita upload binário
// nesses endpoints). Devolve o ID do post/vídeo criado na Página.
func (c *facebookClient) publishMedia(ctx context.Context, mediaURL, caption string, isVideo bool) (string, error) {
	if !c.enabled() {
		return "", fmt.Errorf("facebook client não configurado (FACEBOOK_PAGE_ID/FACEBOOK_ACCESS_TOKEN ausentes)")
	}
	form := url.Values{}
	edge := "photos"
	if isVideo {
		edge = "videos"
		form.Set("file_url", mediaURL)
		form.Set("description", caption)
	} else {
		form.Set("url", mediaURL)
		form.Set("caption", caption)
	}
	return c.postForm(ctx, fmt.Sprintf("%s/%s", c.pageID, edge), form)
}

// postForm faz um POST form-urlencoded autenticado e devolve o campo "id" da
// resposta — formato comum às chamadas de criação da Graph API (photos,
// videos, media, media_publish).
func (c *facebookClient) postForm(ctx context.Context, path string, form url.Values) (string, error) {
	form.Set("access_token", c.token)
	endpoint := fmt.Sprintf("%s/%s", c.baseURL, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("criar request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("chamar graph api: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("graph api retornou status %d: %s", res.StatusCode, body)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decodificar resposta: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("graph api não devolveu id: %s", body)
	}
	return out.ID, nil
}
