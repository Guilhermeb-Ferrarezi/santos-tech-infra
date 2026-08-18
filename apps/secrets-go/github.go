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

// retryAfterWait interpreta o header Retry-After (segundos ou data HTTP) e
// espera o tempo indicado, respeitando o cancelamento do contexto. Retorna
// ctx.Err() se o contexto for cancelado durante a espera.
func retryAfterWait(ctx context.Context, resp *http.Response, fallback time.Duration) error {
	wait := fallback
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			wait = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(ra); err == nil {
			wait = time.Until(t)
			if wait < 0 {
				wait = 0
			}
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
	}
	return nil
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
//
// O limite geral de 5000 req/h é por token e, em scans grandes, pode ser
// estourado — então em 403/429 (rate limit) o token específico vai pro
// cooldown (ele some do rotation até o Retry-After passar), esperamos e
// tentamos de novo, até 3 tentativas por arquivo (cada uma pode pegar outro
// token do pool, se houver). Arquivo que falha volta como erro e é
// simplesmente pulado — nunca trava o scan.
func (c *GitHubClient) FetchFileContent(ctx context.Context, contentsURL string) (string, error) {
	if contentsURL == "" {
		return "", fmt.Errorf("contentsURL vazia")
	}

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
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

		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			// Só esse token vai pro cooldown — a próxima tentativa pode
			// pegar outro disponível do pool em vez de travar esperando.
			tok.disable(30 * time.Second)
			waitErr := retryAfterWait(ctx, resp, 20*time.Second)
			resp.Body.Close()
			if waitErr != nil {
				return "", waitErr
			}
			continue // estourou o rate limit — esperou, tenta de novo
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("fetch content falhou (%s)", resp.Status)
		}

		var out contentsAPIResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if decodeErr != nil {
			return "", decodeErr
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
	return "", fmt.Errorf("fetch content falhou (rate limit persistente)")
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
