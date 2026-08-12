package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
)

// ghToken é um PAT do pool com seu próprio estado de cooldown — quando o
// GitHub devolve 403/429 pra esse token específico, ele fica de fora do
// rotation até disabledUntil passar, sem travar os outros tokens do pool.
type ghToken struct {
	token         string
	disabledUntil atomic.Int64 // unix nano; 0 = disponível
}

func (t *ghToken) disable(d time.Duration) {
	t.disabledUntil.Store(time.Now().Add(d).UnixNano())
}

func (t *ghToken) coolingDown() bool {
	return time.Now().UnixNano() < t.disabledUntil.Load()
}

// GitHubClient consome a Search API e a Contents API do GitHub com um POOL de
// tokens (1 ou mais PATs via GITHUB_TOKENS). Cada token tem seu próprio
// bucket de rate limit — mais tokens no pool = mais throughput real, não só
// fallback. Os créditos de todos os tokens convergem pros canais
// searchAvail/contentAvail: quem chama SearchCode/FetchFileContent só recebe
// "qual token está livre agora", sem se importar com qual é.
type GitHubClient struct {
	tokens       []*ghToken
	http         *http.Client
	searchAvail  chan *ghToken
	contentAvail chan *ghToken
}

func NewGitHubClient(tokens []string) *GitHubClient {
	c := &GitHubClient{
		http:         &http.Client{Timeout: 20 * time.Second},
		searchAvail:  make(chan *ghToken, len(tokens)),
		contentAvail: make(chan *ghToken, len(tokens)*3),
	}
	for _, tok := range tokens {
		gt := &ghToken{token: tok}
		c.tokens = append(c.tokens, gt)
		c.searchAvail <- gt
		for i := 0; i < 3; i++ {
			c.contentAvail <- gt
		}
		go refillTokenTicker(c.searchAvail, gt, 6500*time.Millisecond) // ~9 req/min por token: conservador p/ code search
		go refillTokenTicker(c.contentAvail, gt, 250*time.Millisecond) // ~4 req/s por token: bem dentro do limite geral de 5000/h
	}
	return c
}

// refillTokenTicker devolve créditos de um token específico ao canal
// compartilhado. Pula o refill enquanto o token está em cooldown (403/429
// recente) — assim ele some do rotation sem exigir lógica extra em quem
// consome o canal.
func refillTokenTicker(bucket chan *ghToken, t *ghToken, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if t.coolingDown() {
			continue
		}
		select {
		case bucket <- t:
		default:
		}
	}
}

type CodeSearchItem struct {
	Path       string `json:"path"`
	HTMLURL    string `json:"html_url"`
	ContentURL string `json:"url"`
	Repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
		Private  bool   `json:"private"`
	} `json:"repository"`
}

type codeSearchResponse struct {
	TotalCount int              `json:"total_count"`
	Items      []CodeSearchItem `json:"items"`
}

// SearchCode pagina os resultados de busca de código, até maxPages (ou o
// teto real de 10 páginas / 1000 resultados do GitHub, o que vier primeiro)
// e chama onPage a cada página recebida.
func (c *GitHubClient) SearchCode(ctx context.Context, keyword string, maxPages int, onPage func(items []CodeSearchItem, totalCount int) error) error {
	if maxPages <= 0 || maxPages > 10 {
		maxPages = 10 // teto real da Search API do GitHub
	}
	for page := 1; page <= maxPages; page++ {
		var tok *ghToken
		select {
		case <-ctx.Done():
			return ctx.Err()
		case tok = <-c.searchAvail:
		}

		q := url.Values{}
		q.Set("q", keyword)
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/search/code?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+tok.token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "secrets-go")

		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			wait := 30 * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil {
					wait = time.Duration(secs) * time.Second
				}
			}
			resp.Body.Close()
			// Só esse token vai pro cooldown — com mais de um no pool, a
			// próxima iteração já pega outro disponível em vez de travar o
			// crawler inteiro esperando esse aqui voltar.
			tok.disable(wait)
			page-- // tenta a mesma página de novo (com outro token, se houver)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return fmt.Errorf("github search falhou (%s): %s", resp.Status, string(body))
		}

		var out codeSearchResponse
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			return err
		}

		if err := onPage(out.Items, out.TotalCount); err != nil {
			return err
		}

		if len(out.Items) < 100 || page*100 >= out.TotalCount {
			break
		}
	}
	return nil
}

type contentsAPIResponse struct {
	Content     string `json:"content"`
	Encoding    string `json:"encoding"`
	DownloadURL string `json:"download_url"`
}

// FetchFileContent baixa o conteúdo real de um arquivo (via Contents API) pra
// verificação — a Search API só serve como pré-filtro barulhento, quem confirma
// se o segredo é real é uma checagem no texto de verdade.
func (c *GitHubClient) FetchFileContent(ctx context.Context, contentsURL string) (string, error) {
	if contentsURL == "" {
		return "", fmt.Errorf("contentsURL vazia")
	}

	var tok *ghToken
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case tok = <-c.contentAvail:
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, contentsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+tok.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "secrets-go")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		wait := 30 * time.Second
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				wait = time.Duration(secs) * time.Second
			}
		}
		tok.disable(wait)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch content falhou (%s)", resp.Status)
	}

	var out contentsAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}

	if out.Encoding == "base64" && out.Content != "" {
		decoded, err := base64.StdEncoding.DecodeString(stripNewlines(out.Content))
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	}

	// arquivo grande demais pra Contents API embutir (sem "content"): não
	// verificamos o texto, quem chamou decide o que fazer com isso.
	return "", fmt.Errorf("conteúdo não disponível diretamente (arquivo grande?)")
}

func stripNewlines(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' && s[i] != '\r' {
			b = append(b, s[i])
		}
	}
	return string(b)
}
