package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// publishMedia cria o container (imagem ou vídeo, a partir de uma URL
// pública — a Graph API busca o arquivo sozinha) e, quando o processamento
// terminar (só relevante para vídeo), publica. Devolve o ID da publicação.
// opts.coverURL só se aplica a vídeo (capa customizada do Reel — sem ela a
// Meta usa o frame 0, que costuma sair borrado); opts.altText só a imagem
// (texto alternativo de acessibilidade).
func (c *instagramClient) publishMedia(ctx context.Context, mediaURL, caption string, isVideo bool, opts publishOptions) (string, error) {
	if !c.enabled() {
		return "", fmt.Errorf("instagram client não configurado (INSTAGRAM_USER_ID/INSTAGRAM_ACCESS_TOKEN ausentes)")
	}
	form := url.Values{}
	form.Set("caption", caption)
	if isVideo {
		form.Set("media_type", "REELS")
		form.Set("video_url", mediaURL)
		if opts.coverURL != "" {
			form.Set("cover_url", opts.coverURL)
		}
	} else {
		form.Set("image_url", mediaURL)
		if opts.altText != "" {
			form.Set("alt_text", opts.altText)
		}
	}
	if opts.locationID != "" {
		form.Set("location_id", opts.locationID)
	}
	creationID, err := c.postForm(ctx, fmt.Sprintf("%s/media", c.userID), form)
	if err != nil {
		return "", fmt.Errorf("criar container de mídia: %w", err)
	}
	if isVideo {
		if err := c.waitMediaFinished(ctx, creationID); err != nil {
			return "", err
		}
	}
	publishForm := url.Values{}
	publishForm.Set("creation_id", creationID)
	mediaID, err := c.postForm(ctx, fmt.Sprintf("%s/media_publish", c.userID), publishForm)
	if err != nil {
		return "", fmt.Errorf("publicar container: %w", err)
	}
	return mediaID, nil
}

// publishCarousel publica um carrossel real (2 a 10 itens, imagem e/ou
// vídeo misturados): cada item vira um container filho (is_carousel_item),
// vídeo espera processar como em publishMedia, depois um container pai
// media_type=CAROUSEL referencia todos os filhos e é publicado. location_id
// não é suportado em containers de carrossel pela Graph API — só nos filhos
// individuais não faria sentido (a localização é do post, não do item), por
// isso fica de fora aqui (diferente de publishMedia).
func (c *instagramClient) publishCarousel(ctx context.Context, items []carouselMediaItem, caption string) (string, error) {
	if !c.enabled() {
		return "", fmt.Errorf("instagram client não configurado (INSTAGRAM_USER_ID/INSTAGRAM_ACCESS_TOKEN ausentes)")
	}
	if len(items) < 2 || len(items) > 10 {
		return "", fmt.Errorf("carrossel precisa de 2 a 10 itens (recebeu %d)", len(items))
	}
	childIDs := make([]string, 0, len(items))
	for i, item := range items {
		form := url.Values{}
		form.Set("is_carousel_item", "true")
		if item.IsVideo {
			form.Set("video_url", item.URL)
		} else {
			form.Set("image_url", item.URL)
		}
		childID, err := c.postForm(ctx, fmt.Sprintf("%s/media", c.userID), form)
		if err != nil {
			return "", fmt.Errorf("criar item %d do carrossel: %w", i+1, err)
		}
		if item.IsVideo {
			if err := c.waitMediaFinished(ctx, childID); err != nil {
				return "", fmt.Errorf("item %d do carrossel: %w", i+1, err)
			}
		}
		childIDs = append(childIDs, childID)
	}
	parentForm := url.Values{}
	parentForm.Set("media_type", "CAROUSEL")
	parentForm.Set("children", strings.Join(childIDs, ","))
	parentForm.Set("caption", caption)
	creationID, err := c.postForm(ctx, fmt.Sprintf("%s/media", c.userID), parentForm)
	if err != nil {
		return "", fmt.Errorf("criar container do carrossel: %w", err)
	}
	publishForm := url.Values{}
	publishForm.Set("creation_id", creationID)
	mediaID, err := c.postForm(ctx, fmt.Sprintf("%s/media_publish", c.userID), publishForm)
	if err != nil {
		return "", fmt.Errorf("publicar carrossel: %w", err)
	}
	return mediaID, nil
}

// waitMediaFinished espera o processamento assíncrono de vídeo terminar
// (status_code IN_PROGRESS -> FINISHED) antes de publicar — publicar um
// container ainda em processamento falha do lado da Meta. ~5min de espera
// total, intervalo curto no início e mais longo depois (vídeos grandes
// demoram minutos, não faz sentido consultar a cada 3s o tempo todo).
func (c *instagramClient) waitMediaFinished(ctx context.Context, creationID string) error {
	delays := []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second,
		20 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second, 60 * time.Second, 60 * time.Second}
	endpoint := fmt.Sprintf("%s/%s?fields=status_code&access_token=%s", c.baseURL, creationID, url.QueryEscape(c.token))
	for _, d := range delays {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("criar request de status: %w", err)
		}
		res, err := c.client.Do(req)
		if err != nil {
			return fmt.Errorf("consultar status do container: %w", err)
		}
		var out struct {
			StatusCode string `json:"status_code"`
		}
		decodeErr := json.NewDecoder(res.Body).Decode(&out)
		res.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("decodificar status do container: %w", decodeErr)
		}
		switch out.StatusCode {
		case "FINISHED":
			return nil
		case "ERROR", "EXPIRED":
			return fmt.Errorf("processamento do vídeo falhou (status %s)", out.StatusCode)
		}
	}
	return fmt.Errorf("vídeo não terminou de processar a tempo (timeout)")
}

// postForm faz um POST form-urlencoded autenticado e devolve o campo "id" da
// resposta — formato comum às chamadas de criação da Graph API (media, media_publish).
func (c *instagramClient) postForm(ctx context.Context, path string, form url.Values) (string, error) {
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
