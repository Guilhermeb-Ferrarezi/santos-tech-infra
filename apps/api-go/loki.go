package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type lokiClient struct {
	base       string
	http       *http.Client
	maxRange   time.Duration
	mu         sync.Mutex
	appLabel   string
	appLabelAt time.Time
}

func newLokiClient(base string) *lokiClient {
	return &lokiClient{
		base:     strings.TrimRight(base, "/"),
		http:     &http.Client{Timeout: 20 * time.Second},
		maxRange: 7 * 24 * time.Hour,
	}
}

func (c *lokiClient) enabled() bool { return c.base != "" }

type lokiEntry struct {
	TS     string            `json:"ts"`
	Time   string            `json:"time"`
	Labels map[string]string `json:"labels"`
	Line   string            `json:"line"`
	Fields map[string]any    `json:"fields,omitempty"`
}

type lokiPage struct {
	Entries     []lokiEntry `json:"entries"`
	OlderCursor string      `json:"olderCursor,omitempty"`
	NewerCursor string      `json:"newerCursor,omitempty"`
	HasOlder    bool        `json:"hasOlder"`
	HasNewer    bool        `json:"hasNewer"`
}

type lokiPageParams struct {
	Start  time.Time
	End    time.Time
	Limit  int
	Before string
	After  string
}

type lokiQueryFilters struct {
	Apps        []string
	Level       string
	StatusClass string
	StatusCode  int
	Method      string
	Path        string
	RequestID   string
	Search      string
}

var lokiRangePresets = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// getLokiAppLabel descobre qual label o Loki usa para nomear serviços, tentando
// candidatos em ordem. O resultado fica cacheado por 5 minutos; padrão: "app".
func (c *lokiClient) getLokiAppLabel(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appLabel != "" && time.Since(c.appLabelAt) < 5*time.Minute {
		return c.appLabel
	}
	for _, candidate := range []string{"app", "service_name", "container_name", "job"} {
		vals, err := c.lokiLabelValues(ctx, candidate)
		if err == nil && len(vals) > 0 {
			c.appLabel = candidate
			c.appLabelAt = time.Now()
			return candidate
		}
	}
	c.appLabel = "app"
	c.appLabelAt = time.Now()
	return "app"
}

func (c *lokiClient) lokiQuery(ctx context.Context, logql string, p lokiPageParams) (lokiPage, error) {
	if !c.enabled() {
		return lokiPage{}, fmt.Errorf("loki desabilitado")
	}
	limit := p.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	start, end := p.Start, p.End
	direction := "backward"
	switch {
	case p.After != "":
		if ns, ok := lokiParseNs(p.After); ok {
			start = time.Unix(0, ns+1)
		}
		direction = "forward"
	case p.Before != "":
		if ns, ok := lokiParseNs(p.Before); ok {
			end = time.Unix(0, ns)
		}
	}
	if end.Sub(start) > c.maxRange {
		start = end.Add(-c.maxRange)
	}
	q := url.Values{}
	q.Set("query", logql)
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("direction", direction)

	var body struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.lokiGet(ctx, "/loki/api/v1/query_range", q, &body); err != nil {
		return lokiPage{}, err
	}
	entries := make([]lokiEntry, 0, limit)
	for _, st := range body.Data.Result {
		for _, v := range st.Values {
			entries = append(entries, newLokiEntryFromRaw(v[0], v[1], st.Stream))
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].TS > entries[j].TS })
	full := len(entries) == limit
	page := lokiPage{Entries: entries}
	if len(entries) > 0 {
		page.NewerCursor = entries[0].TS
		page.OlderCursor = entries[len(entries)-1].TS
	}
	switch {
	case direction == "forward":
		page.HasOlder = true
		page.HasNewer = full
	default:
		page.HasOlder = full
		page.HasNewer = p.Before != ""
	}
	return page, nil
}

func (c *lokiClient) lokiLabelValues(ctx context.Context, label string) ([]string, error) {
	if !c.enabled() {
		return nil, fmt.Errorf("loki desabilitado")
	}
	var body struct {
		Data []string `json:"data"`
	}
	if err := c.lokiGet(ctx, "/loki/api/v1/label/"+url.PathEscape(label)+"/values", nil, &body); err != nil {
		return nil, err
	}
	sort.Strings(body.Data)
	return body.Data, nil
}

func (c *lokiClient) lokiGet(ctx context.Context, path string, q url.Values, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("loki respondeu %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func newLokiEntryFromRaw(ts, line string, stream map[string]string) lokiEntry {
	e := lokiEntry{TS: ts, Line: line, Labels: stream}
	if ns, ok := lokiParseNs(ts); ok {
		e.Time = time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
	}
	if s := strings.TrimSpace(line); strings.HasPrefix(s, "{") {
		var f map[string]any
		if json.Unmarshal([]byte(s), &f) == nil {
			e.Fields = f
		}
	}
	return e
}

func lokiParseNs(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n, err == nil
}

// buildLokiQL monta uma expressão LogQL a partir de filtros estruturados.
// appLabel é o nome do label de serviço descoberto pelo getLokiAppLabel.
func buildLokiQL(q lokiQueryFilters, appLabel string) string {
	if appLabel == "" {
		appLabel = "app"
	}
	var sb strings.Builder
	if apps := lokiNonEmpty(q.Apps); len(apps) > 0 {
		sb.WriteString(`{` + appLabel + `=~` + lokiQuote(lokiRegexAlt(apps)) + `}`)
	} else {
		sb.WriteString(`{` + appLabel + `=~".+"}`)
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		sb.WriteString(` |= ` + lokiQuote(s))
	}
	needJSON := q.Level != "" || q.StatusClass != "" || q.StatusCode != 0 ||
		q.Method != "" || strings.TrimSpace(q.Path) != "" || q.RequestID != ""
	if needJSON {
		sb.WriteString(` | json`)
		if lv := strings.TrimSpace(q.Level); lv != "" {
			sb.WriteString(` | level=~` + lokiQuote("(?i)^"+regexp.QuoteMeta(lv)+"$"))
		}
		if q.RequestID != "" {
			sb.WriteString(` | request_id=` + lokiQuote(strings.TrimSpace(q.RequestID)))
		}
		if q.Method != "" {
			sb.WriteString(` | method=` + lokiQuote(strings.ToUpper(strings.TrimSpace(q.Method))))
		}
		if p := strings.TrimSpace(q.Path); p != "" {
			sb.WriteString(` | path=~` + lokiQuote(".*"+regexp.QuoteMeta(p)+".*"))
		}
		switch {
		case q.StatusCode != 0:
			sb.WriteString(` | status=` + lokiQuote(strconv.Itoa(q.StatusCode)))
		case q.StatusClass == "2xx":
			sb.WriteString(` | status>=200 | status<300`)
		case q.StatusClass == "4xx":
			sb.WriteString(` | status>=400 | status<500`)
		case q.StatusClass == "5xx":
			sb.WriteString(` | status>=500 | status<600`)
		}
	}
	return sb.String()
}

func lokiRegexAlt(vals []string) string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, regexp.QuoteMeta(v))
	}
	return strings.Join(out, "|")
}

func lokiQuote(s string) string { return strconv.Quote(s) }

func lokiNonEmpty(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}
