package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// NotionClient lê e grava na base "Agenda de Aulas" do Notion via API REST, com
// um token de integração escopado. O LLM NUNCA acessa o Notion — só este código.
type NotionClient struct {
	token string
	dbID  string
	http  *http.Client

	// cache da leitura da agenda (TTL curto) para não consultar a cada mensagem.
	mu      sync.Mutex
	cache   []ScheduleEntry
	cacheAt time.Time
	ttl     time.Duration
}

// NewNotionClient cria o cliente. Se token/dbID estiverem vazios, Enabled()=false
// e as operações degradam (sem erro fatal).
func NewNotionClient(token, dbID string) *NotionClient {
	return &NotionClient{
		token: strings.TrimSpace(token),
		dbID:  strings.TrimSpace(dbID),
		http:  &http.Client{Timeout: 15 * time.Second},
		ttl:   5 * time.Minute,
	}
}

// Enabled indica se há credenciais configuradas.
func (c *NotionClient) Enabled() bool {
	return c != nil && c.token != "" && c.dbID != ""
}

// Schedule retorna as aulas já agendadas (cacheadas por TTL). Em erro, devolve o
// último cache bom (ou nil) — best-effort.
func (c *NotionClient) Schedule(ctx context.Context) []ScheduleEntry {
	if !c.Enabled() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cache != nil && time.Since(c.cacheAt) < c.ttl {
		return c.cache
	}
	entries, err := c.fetchSchedule(ctx)
	if err != nil {
		return c.cache // mantém o último bom
	}
	c.cache = entries
	c.cacheAt = time.Now()
	return entries
}

func (c *NotionClient) fetchSchedule(ctx context.Context) ([]ScheduleEntry, error) {
	url := fmt.Sprintf("https://api.notion.com/v1/databases/%s/query", c.dbID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("notion: query status %d: %s", resp.StatusCode, string(raw))
	}

	var out struct {
		Results []struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("notion: query unmarshal: %w", err)
	}

	entries := make([]ScheduleEntry, 0, len(out.Results))
	for _, r := range out.Results {
		entries = append(entries, ScheduleEntry{
			Aula:      notionTitle(r.Properties["Aula"]),
			Dia:       notionSelect(r.Properties["Dia"]),
			Horario:   notionText(r.Properties["Horário"]),
			Periodo:   notionSelect(r.Properties["Período"]),
			Professor: notionSelect(r.Properties["Professor"]),
			Conteudo:  notionSelect(r.Properties["Conteúdo"]),
			Aluno:     notionMultiSelect(r.Properties["Aluno"]),
		})
	}
	return entries, nil
}

// CreateBooking grava uma nova aula na agenda (após confirmação do admin).
func (c *NotionClient) CreateBooking(ctx context.Context, b Booking) error {
	if !c.Enabled() {
		return fmt.Errorf("notion: não configurado")
	}

	props := map[string]any{
		"Aula": map[string]any{
			"title": []any{map[string]any{"text": map[string]any{"content": b.Title}}},
		},
	}
	if b.Dia != "" {
		props["Dia"] = map[string]any{"select": map[string]any{"name": b.Dia}}
	}
	if b.Horario != "" {
		props["Horário"] = map[string]any{
			"rich_text": []any{map[string]any{"text": map[string]any{"content": b.Horario}}},
		}
	}
	if b.Periodo != "" {
		props["Período"] = map[string]any{"select": map[string]any{"name": b.Periodo}}
	}
	if b.Data != "" {
		props["Data"] = map[string]any{"date": map[string]any{"start": b.Data}}
	}
	if b.Conteudo != "" {
		props["Conteúdo"] = map[string]any{"select": map[string]any{"name": b.Conteudo}}
	}

	body := map[string]any{
		"parent":     map[string]any{"database_id": c.dbID},
		"properties": props,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notion: marshal booking: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.notion.com/v1/pages", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("notion: create page status %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

func (c *NotionClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", "2022-06-28")
	req.Header.Set("Content-Type", "application/json")
}

// ── parsers de propriedade do Notion ──────────────────────────────────────────

func notionSelect(raw json.RawMessage) string {
	var p struct {
		Select *struct {
			Name string `json:"name"`
		} `json:"select"`
	}
	if json.Unmarshal(raw, &p) == nil && p.Select != nil {
		return p.Select.Name
	}
	return ""
}

func notionText(raw json.RawMessage) string {
	var p struct {
		RichText []struct {
			PlainText string `json:"plain_text"`
		} `json:"rich_text"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	var sb strings.Builder
	for _, t := range p.RichText {
		sb.WriteString(t.PlainText)
	}
	return sb.String()
}

func notionTitle(raw json.RawMessage) string {
	var p struct {
		Title []struct {
			PlainText string `json:"plain_text"`
		} `json:"title"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	var sb strings.Builder
	for _, t := range p.Title {
		sb.WriteString(t.PlainText)
	}
	return sb.String()
}

func notionMultiSelect(raw json.RawMessage) string {
	var p struct {
		MultiSelect []struct {
			Name string `json:"name"`
		} `json:"multi_select"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	names := make([]string, 0, len(p.MultiSelect))
	for _, m := range p.MultiSelect {
		names = append(names, m.Name)
	}
	return strings.Join(names, ", ")
}
