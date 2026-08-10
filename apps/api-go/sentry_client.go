package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// sentryClient consulta a API do Sentry (sentry.io) pra listar issues (erros
// capturados) de todos os projetos do ecossistema numa visão só, sem precisar
// abrir o sentry.io. Só leitura — resolver/ignorar issue continua no Sentry.
type sentryClient struct {
	org        string
	token      string
	http       *http.Client
	projectIDs []string // IDs numéricos dos 14 projetos (um por serviço)
}

// sentryProjectIDs mapeia cada serviço ao ID numérico do seu projeto no
// Sentry (extraído do path do DSN). Atualize se um projeto novo for criado
// em sentry.io/organizations/santos-techrp.
var sentryProjectIDs = map[string]string{
	"api-go":        "4511887960834048",
	"agent-go":      "4511887960965120",
	"bot-go":        "4511887961030656",
	"cron-go":       "4511887961030657",
	"mcp-go":        "4511887961096192",
	"payments-go":   "4511887961161728",
	"auto-fixer":    "4511887961227264",
	"auth-web":      "4511887961292800",
	"pay-web":       "4511887961358336",
	"whats":         "4511887951003648",
	"home-page":     "4511887961423872",
	"blog-web":      "4511887961489408",
	"dashboard-web": "4511887961554944",
	"dashboard-api": "4511887961620480",
}

func newSentryClient(org, token string) *sentryClient {
	ids := make([]string, 0, len(sentryProjectIDs))
	for _, id := range sentryProjectIDs {
		ids = append(ids, id)
	}
	return &sentryClient{
		org:        org,
		token:      token,
		http:       &http.Client{Timeout: 15 * time.Second},
		projectIDs: ids,
	}
}

func (c *sentryClient) enabled() bool { return c.org != "" && c.token != "" }

type sentryIssue struct {
	ID        string    `json:"id"`
	ShortID   string    `json:"shortId"`
	Title     string    `json:"title"`
	Culprit   string    `json:"culprit"`
	Level     string    `json:"level"`
	Status    string    `json:"status"`
	Count     string    `json:"count"`
	UserCount int       `json:"userCount"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Permalink string    `json:"permalink"`
	Project   struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"project"`
}

type sentryIssuesParams struct {
	Project     string // slug de um serviço específico ("" = todos os 14)
	Status      string // "unresolved" (default) | "resolved" | "ignored" | "" (todas)
	Search      string // texto livre, some ao "query" do Sentry
	StatsPeriod string // "24h" | "14d" | "90d" — default "14d"
	Limit       int    // default 25, teto 100
}

// listIssues devolve as issues dos projetos do ecossistema, mais recentes
// primeiro. Se Project for um slug conhecido, filtra só aquele projeto.
func (c *sentryClient) listIssues(ctx context.Context, p sentryIssuesParams) ([]sentryIssue, error) {
	ids := c.projectIDs
	if p.Project != "" {
		id, ok := sentryProjectIDs[p.Project]
		if !ok {
			return nil, fmt.Errorf("projeto desconhecido: %s", p.Project)
		}
		ids = []string{id}
	}

	q := url.Values{}
	for _, id := range ids {
		q.Add("project", id)
	}
	statsPeriod := p.StatsPeriod
	if statsPeriod == "" {
		statsPeriod = "14d"
	}
	q.Set("statsPeriod", statsPeriod)
	q.Set("sort", "date")
	limit := p.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	q.Set("limit", strconv.Itoa(limit))

	query := strings.TrimSpace(p.Search)
	status := p.Status
	if status == "" {
		status = "unresolved"
	}
	if status != "all" {
		if query != "" {
			query = "is:" + status + " " + query
		} else {
			query = "is:" + status
		}
	}
	if query != "" {
		q.Set("query", query)
	}

	reqURL := "https://sentry.io/api/0/organizations/" + c.org + "/issues/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sentry respondeu %d", resp.StatusCode)
	}

	var issues []sentryIssue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// sentryProjectSlugs devolve os slugs conhecidos (pra popular o filtro de
// projeto na tela), em ordem estável.
func sentryProjectSlugs() []string {
	slugs := make([]string, 0, len(sentryProjectIDs))
	for slug := range sentryProjectIDs {
		slugs = append(slugs, slug)
	}
	return slugs
}
