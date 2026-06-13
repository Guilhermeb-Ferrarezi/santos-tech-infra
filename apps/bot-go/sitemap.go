package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SitemapCache busca e cacheia as URLs do sitemap.xml do site oficial. Essas
// URLs são as únicas páginas que o bot pode consultar via WebFetch — assim, ao
// adicionar uma rota no sitemap, o bot passa a poder usá-la automaticamente.
type SitemapCache struct {
	url    string
	http   *http.Client
	ttl    time.Duration
	mu     sync.Mutex
	urls   []string
	loaded time.Time
}

// NewSitemapCache cria o cache para <siteURL>/sitemap.xml (TTL de 30 min).
func NewSitemapCache(siteURL string) *SitemapCache {
	return &SitemapCache{
		url:  strings.TrimRight(siteURL, "/") + "/sitemap.xml",
		http: &http.Client{Timeout: 10 * time.Second},
		ttl:  30 * time.Minute,
	}
}

// URLs retorna as URLs do sitemap (cacheadas por TTL). Em falha, devolve o último
// cache bom; se nunca carregou, devolve apenas a home como fallback mínimo.
func (c *SitemapCache) URLs(ctx context.Context) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.urls != nil && time.Since(c.loaded) < c.ttl {
		return c.urls
	}

	fetched, err := c.fetch(ctx)
	if err != nil || len(fetched) == 0 {
		if c.urls != nil {
			return c.urls // mantém o último bom em caso de erro temporário
		}
		base := strings.TrimSuffix(c.url, "/sitemap.xml")
		return []string{base + "/"}
	}

	c.urls = fetched
	c.loaded = time.Now()
	return fetched
}

// fetch baixa e parseia o sitemap.xml, retornando as <loc>.
func (c *SitemapCache) fetch(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sitemap: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var doc struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("sitemap: parse: %w", err)
	}

	out := make([]string, 0, len(doc.URLs))
	for _, u := range doc.URLs {
		if loc := strings.TrimSpace(u.Loc); loc != "" {
			out = append(out, loc)
		}
	}
	return out, nil
}
