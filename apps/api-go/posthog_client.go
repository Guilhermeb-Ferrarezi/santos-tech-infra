package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// posthogClient consulta a Query API (HogQL) e a API de Session Recordings da
// PostHog pra alimentar a tela de Analytics sem precisar abrir o
// app.posthog.com. Só leitura.
type posthogClient struct {
	host      string
	projectID string
	token     string
	http      *http.Client
}

func newPostHogClient(host, projectID, token string) *posthogClient {
	return &posthogClient{
		host:      host,
		projectID: projectID,
		token:     token,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *posthogClient) enabled() bool { return c.projectID != "" && c.token != "" }

func (c *posthogClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("posthog respondeu %d em %s: %s", resp.StatusCode, path, string(body))
	}
	return body, nil
}

// hogqlRow é uma linha genérica de resultado — a ordem das colunas bate com
// a query que cada handler montou.
type hogqlRow []any

// query executa uma HogQLQuery na PostHog e devolve as linhas cruas. A query
// é sempre montada pelo código (nunca a partir de input de usuário sem passar
// por whitelist antes — ver posthogRangeDays), pra não abrir injeção HogQL.
func (c *posthogClient) query(ctx context.Context, hogql string) ([]hogqlRow, error) {
	body, err := json.Marshal(map[string]any{
		"query": map[string]string{"kind": "HogQLQuery", "query": hogql},
	})
	if err != nil {
		return nil, err
	}
	reqURL := fmt.Sprintf("%s/api/projects/%s/query/", c.host, c.projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("posthog respondeu %d", resp.StatusCode)
	}
	var out struct {
		Results []hogqlRow `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// ── Session Recordings (replay) ─────────────────────────────────────────────

// listRecordings repassa a listagem de gravações da PostHog quase sem
// alteração (o shape delas já serve bem ao front: duration, start_url,
// click_count, etc.). recentFirst já é o comportamento padrão da API deles.
func (c *posthogClient) listRecordings(ctx context.Context, days, limit int) (json.RawMessage, error) {
	path := fmt.Sprintf("/api/projects/%s/session_recordings/?limit=%d&date_from=-%dd", c.projectID, limit, days)
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

type recordingSource struct {
	Source    string `json:"source"`
	BlobKey   string `json:"blob_key"`
	StartTime string `json:"start_timestamp"`
	EndTime   string `json:"end_timestamp"`
}

// recordingEvents busca TODOS os blobs de uma gravação (numa única chamada,
// pedindo o range inteiro de blob_key) e devolve os eventos rrweb já
// descompactados, prontos pro rrweb-player. A PostHog compacta o conteúdo dos
// snapshots em gzip — às vezes o campo `data` inteiro (snapshot completo),
// às vezes só sub-campos específicos dele (adds/attributes/removes/texts nas
// mutações incrementais). decompressTree lida com os dois casos sem precisar
// conhecer o schema exato: qualquer string que descompacte como gzip válido
// é substituída pelo valor descompactado.
func (c *posthogClient) recordingEvents(ctx context.Context, id string) ([]any, error) {
	sourcesBody, err := c.get(ctx, fmt.Sprintf("/api/projects/%s/session_recordings/%s/snapshots/", c.projectID, id))
	if err != nil {
		return nil, err
	}
	var sources struct {
		Sources []recordingSource `json:"sources"`
	}
	if err := json.Unmarshal(sourcesBody, &sources); err != nil {
		return nil, err
	}
	blobKeys := make([]string, 0, len(sources.Sources))
	for _, s := range sources.Sources {
		if s.Source == "blob_v2" {
			blobKeys = append(blobKeys, s.BlobKey)
		}
	}
	if len(blobKeys) == 0 {
		return []any{}, nil
	}
	// blob_key é sempre um índice numérico crescente ("0","1","2"...) — o
	// primeiro e o último da lista bastam pra pedir o range inteiro numa
	// chamada só, em vez de uma requisição por chunk.
	startKey, endKey := blobKeys[0], blobKeys[len(blobKeys)-1]
	ndjson, err := c.get(ctx, fmt.Sprintf(
		"/api/projects/%s/session_recordings/%s/snapshots/?source=blob_v2&start_blob_key=%s&end_blob_key=%s",
		c.projectID, id, startKey, endKey,
	))
	if err != nil {
		return nil, err
	}

	events := make([]any, 0, 1024)
	sc := bufio.NewScanner(bytes.NewReader(ndjson))
	sc.Buffer(make([]byte, 1<<20), 32<<20) // linhas de snapshot completo podem ser grandes
	for sc.Scan() {
		var pair []json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &pair); err != nil || len(pair) != 2 {
			continue // linha corrompida/inesperada — ignora, não derruba a gravação inteira
		}
		var ev any
		if err := json.Unmarshal(pair[1], &ev); err != nil {
			continue
		}
		events = append(events, decompressTree(ev))
	}
	return events, sc.Err()
}

// tryGunzipString tenta reconstituir bytes crus a partir de uma string JS
// "binária" (um byte por rune — é assim que a PostHog serializa gzip dentro
// de JSON) e descompactar como gzip. ok=false se não parecer/não for gzip.
func tryGunzipString(s string) (json.RawMessage, bool) {
	raw := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 255 {
			return nil, false
		}
		raw = append(raw, byte(r))
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return nil, false
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, false
	}
	out, err := io.ReadAll(gz)
	if err != nil {
		return nil, false
	}
	var probe any
	if json.Unmarshal(out, &probe) == nil {
		return json.RawMessage(out), true
	}
	b, err := json.Marshal(string(out))
	if err != nil {
		return nil, false
	}
	return json.RawMessage(b), true
}

// decompressTree percorre recursivamente um valor JSON genérico; toda string
// que descompacta como gzip válido é substituída pelo valor descompactado
// (parseado como JSON quando possível, recursivamente). Não assume nomes de
// campo nem event type/source específicos — robusto a mudanças de schema da
// PostHog, desde que continuem usando essa mesma convenção (string gzip).
func decompressTree(v any) any {
	switch t := v.(type) {
	case string:
		if raw, ok := tryGunzipString(t); ok {
			var inner any
			if json.Unmarshal(raw, &inner) == nil {
				return decompressTree(inner)
			}
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = decompressTree(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = decompressTree(val)
		}
		return out
	default:
		return v
	}
}
