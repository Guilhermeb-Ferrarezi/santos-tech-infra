package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// notionVersion — usamos a API com data sources (database multi-source).
const notionVersion = "2025-09-03"

// brLocation — fuso fixo do Brasil (UTC-3, sem horário de verão). FixedZone evita
// depender de tzdata, ausente na imagem distroless.
var brLocation = time.FixedZone("BRT", -3*60*60)

// NotionClient lê e grava no data source "Agenda — Aulas Experimentais" do Notion
// via API REST, com um token de integração escopado. O LLM NUNCA acessa o Notion —
// só este código.
type NotionClient struct {
	token string
	dsID  string // data source id (não o database id)
	http  *http.Client

	// cache da leitura da agenda (TTL curto) para não consultar a cada mensagem.
	mu      sync.Mutex
	cache   []ScheduleEntry
	cacheAt time.Time
	ttl     time.Duration
}

// NewNotionClient cria o cliente. Se token/dsID estiverem vazios, Enabled()=false
// e as operações degradam (sem erro fatal).
func NewNotionClient(token, dsID string) *NotionClient {
	return &NotionClient{
		token: strings.TrimSpace(token),
		dsID:  strings.TrimSpace(dsID),
		http:  &http.Client{Timeout: 15 * time.Second},
		ttl:   5 * time.Minute,
	}
}

// Enabled indica se há credenciais configuradas.
func (c *NotionClient) Enabled() bool {
	return c != nil && c.token != "" && c.dsID != ""
}

// Schedule retorna as aulas experimentais já agendadas (cacheadas por TTL). Em erro,
// devolve o último cache bom (ou nil) — best-effort.
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
	url := fmt.Sprintf("https://api.notion.com/v1/data_sources/%s/query", c.dsID)
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
			ID         string                     `json:"id"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("notion: query unmarshal: %w", err)
	}

	// Corta o que não ocupa mais um horário: aulas passadas e as marcadas como
	// Faltou/Remarcou (o slot voltou a ficar livre).
	todayStart := time.Now().In(brLocation).Truncate(24 * time.Hour)

	entries := make([]ScheduleEntry, 0, len(out.Results))
	for _, r := range out.Results {
		status := notionStatus(r.Properties["Status"])
		if status == "Faltou" || status == "Remarcou" {
			continue
		}
		start := notionDateStart(r.Properties["Data e hora"])
		if t, ok := parseNotionTime(start); ok && t.Before(todayStart) {
			continue // já passou
		}
		entries = append(entries, ScheduleEntry{
			PageID:    r.ID,
			Aluno:     notionTitle(r.Properties["Aluno/Responsável"]),
			DataHora:  start,
			Display:   formatBRDateTime(start),
			Status:    status,
			Professor: notionPeopleNames(r.Properties["Professor(a)"]),
			WhatsApp:  notionPhone(r.Properties["WhatsApp"]),
		})
	}
	return entries, nil
}

// CreateBooking grava uma nova aula experimental na agenda (após confirmação do admin).
func (c *NotionClient) CreateBooking(ctx context.Context, b Booking) error {
	if !c.Enabled() {
		return fmt.Errorf("notion: não configurado")
	}

	status := b.Status
	if status == "" {
		status = "Confirmar"
	}

	props := map[string]any{
		"Aluno/Responsável": map[string]any{
			"title": []any{map[string]any{"text": map[string]any{"content": b.Aluno}}},
		},
		"Status": map[string]any{"status": map[string]any{"name": status}},
	}
	if b.WhatsApp != "" {
		props["WhatsApp"] = map[string]any{"phone_number": b.WhatsApp}
	}
	if b.DataHora != "" {
		props["Data e hora"] = map[string]any{"date": map[string]any{"start": b.DataHora}}
	}

	body := map[string]any{
		"parent":     map[string]any{"type": "data_source_id", "data_source_id": c.dsID},
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
	// Invalida o cache pra o próximo Schedule() já refletir o novo agendamento.
	c.mu.Lock()
	c.cache = nil
	c.mu.Unlock()
	return nil
}

// UpdateBookingDateTime atualiza a propriedade "Data e hora" de uma página de aula
// já existente (remarcação). iso é RFC3339 com offset (ex.: vindo de ResolveBookingDateTime).
func (c *NotionClient) UpdateBookingDateTime(ctx context.Context, pageID, iso string) error {
	if !c.Enabled() {
		return fmt.Errorf("notion: não configurado")
	}
	if pageID == "" {
		return fmt.Errorf("notion: pageID vazio")
	}

	body := map[string]any{
		"properties": map[string]any{
			"Data e hora": map[string]any{"date": map[string]any{"start": iso}},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notion: marshal update: %w", err)
	}

	url := "https://api.notion.com/v1/pages/" + pageID
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(payload))
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
		return fmt.Errorf("notion: update page status %d: %s", resp.StatusCode, string(raw))
	}
	// Invalida o cache pra o próximo Schedule() refletir o novo horário.
	c.mu.Lock()
	c.cache = nil
	c.mu.Unlock()
	return nil
}

func (c *NotionClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", notionVersion)
	req.Header.Set("Content-Type", "application/json")
}

// ── resolução de data/hora ─────────────────────────────────────────────────────

var weekdaysPT = map[string]time.Weekday{
	"domingo": time.Sunday, "dom": time.Sunday,
	"segunda": time.Monday, "segunda-feira": time.Monday, "seg": time.Monday,
	"terca": time.Tuesday, "terça": time.Tuesday, "terca-feira": time.Tuesday, "terça-feira": time.Tuesday, "ter": time.Tuesday,
	"quarta": time.Wednesday, "quarta-feira": time.Wednesday, "qua": time.Wednesday,
	"quinta": time.Thursday, "quinta-feira": time.Thursday, "qui": time.Thursday,
	"sexta": time.Friday, "sexta-feira": time.Friday, "sex": time.Friday,
	"sabado": time.Saturday, "sábado": time.Saturday, "sab": time.Saturday, "sáb": time.Saturday,
}

// ResolveBookingDateTime monta um datetime ISO 8601 (com offset -03:00) a partir do
// dia ("terça", "17/06", "2026-06-17", "hoje", "amanhã") e da hora ("19h30", "19:30")
// coletados na conversa. Retorna ok=false se não der pra resolver com confiança.
func ResolveBookingDateTime(day, tm string, now time.Time) (string, bool) {
	hh, mm, okT := parseClock(tm)
	if !okT {
		return "", false
	}
	now = now.In(brLocation)
	y, mo, d, okD := resolveDay(day, now)
	if !okD {
		return "", false
	}
	res := time.Date(y, mo, d, hh, mm, 0, 0, brLocation)
	return res.Format(time.RFC3339), true
}

// parseClock extrai hora e minuto de "19h30", "19:30", "19h", "19", "9h".
func parseClock(s string) (int, int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "hs", "h")
	s = strings.ReplaceAll(s, "horas", "h")
	s = strings.ReplaceAll(s, "hora", "h")
	sep := strings.IndexAny(s, "h:")
	hStr, mStr := s, ""
	if sep >= 0 {
		hStr = s[:sep]
		mStr = strings.Trim(s[sep+1:], " h")
	}
	hStr = strings.TrimSpace(hStr)
	h, err := strconv.Atoi(onlyDigits(hStr))
	if err != nil || h < 0 || h > 23 {
		return 0, 0, false
	}
	m := 0
	if d := onlyDigits(mStr); d != "" {
		m, _ = strconv.Atoi(d)
	}
	if m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// resolveDay devolve ano/mês/dia a partir de um dia em linguagem natural ou data.
func resolveDay(day string, now time.Time) (int, time.Month, int, bool) {
	s := strings.ToLower(strings.TrimSpace(day))
	switch s {
	case "hoje":
		return now.Year(), now.Month(), now.Day(), true
	case "amanha", "amanhã":
		t := now.AddDate(0, 0, 1)
		return t.Year(), t.Month(), t.Day(), true
	}
	// Data ISO YYYY-MM-DD
	if t, err := time.ParseInLocation("2006-01-02", s, brLocation); err == nil {
		return t.Year(), t.Month(), t.Day(), true
	}
	// Data DD/MM ou DD/MM/YYYY
	if y, mo, d, ok := parseSlashDate(s, now); ok {
		return y, mo, d, true
	}
	// Dia da semana → próxima ocorrência. Se cair em HOJE (ex.: "sábado" dito num
	// sábado), pula pra semana que vem: agendar é sempre pra frente, e "hoje" tem
	// palavra própria. Evita gravar a aula no dia errado.
	key := strings.Fields(s)
	if len(key) > 0 {
		if wd, ok := weekdaysPT[key[0]]; ok {
			ahead := (int(wd) - int(now.Weekday()) + 7) % 7
			if ahead == 0 {
				ahead = 7
			}
			t := now.AddDate(0, 0, ahead)
			return t.Year(), t.Month(), t.Day(), true
		}
	}
	return 0, 0, 0, false
}

// parseSlashDate trata "17/06" (ano corrente) e "17/06/2026".
func parseSlashDate(s string, now time.Time) (int, time.Month, int, bool) {
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return 0, 0, 0, false
	}
	d, err1 := strconv.Atoi(onlyDigits(parts[0]))
	mo, err2 := strconv.Atoi(onlyDigits(parts[1]))
	if err1 != nil || err2 != nil || d < 1 || d > 31 || mo < 1 || mo > 12 {
		return 0, 0, 0, false
	}
	y := now.Year()
	if len(parts) >= 3 {
		if yy, err := strconv.Atoi(onlyDigits(parts[2])); err == nil && yy > 0 {
			if yy < 100 {
				yy += 2000
			}
			y = yy
		}
	} else if time.Month(mo) < now.Month() {
		y++ // mês já passou neste ano → assume o ano que vem
	}
	return y, time.Month(mo), d, true
}

// formatBRDateTime formata um ISO em "ter 17/06 às 19:30". Vazio se não parsear.
func formatBRDateTime(iso string) string {
	t, ok := parseNotionTime(iso)
	if !ok {
		return iso
	}
	t = t.In(brLocation)
	wd := []string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"}[t.Weekday()]
	return fmt.Sprintf("%s %02d/%02d às %02d:%02d", wd, t.Day(), int(t.Month()), t.Hour(), t.Minute())
}

// parseNotionTime aceita datetime com hora (RFC3339) ou data pura (YYYY-MM-DD).
func parseNotionTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02", s, brLocation); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ── parsers de propriedade do Notion ──────────────────────────────────────────

func notionStatus(raw json.RawMessage) string {
	var p struct {
		Status *struct {
			Name string `json:"name"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &p) == nil && p.Status != nil {
		return p.Status.Name
	}
	return ""
}

func notionDateStart(raw json.RawMessage) string {
	var p struct {
		Date *struct {
			Start string `json:"start"`
		} `json:"date"`
	}
	if json.Unmarshal(raw, &p) == nil && p.Date != nil {
		return p.Date.Start
	}
	return ""
}

func notionPhone(raw json.RawMessage) string {
	var p struct {
		PhoneNumber string `json:"phone_number"`
	}
	if json.Unmarshal(raw, &p) == nil {
		return p.PhoneNumber
	}
	return ""
}

func notionPeopleNames(raw json.RawMessage) string {
	var p struct {
		People []struct {
			Name string `json:"name"`
		} `json:"people"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	names := make([]string, 0, len(p.People))
	for _, m := range p.People {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	return strings.Join(names, ", ")
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
