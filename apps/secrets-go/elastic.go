package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const hitsIndex = "dotfy-hits"

type ElasticClient struct {
	baseURL string
	http    *http.Client
}

func NewElasticClient(baseURL string) *ElasticClient {
	return &ElasticClient{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 10 * time.Second}}
}

// Ping confere se o Elasticsearch está respondendo — usado pelo readiness
// probe (/ready, ver server.go). Não faz parte do dotfy-scanner original;
// adicionado porque o serviço original não tinha readiness check nenhum.
func (e *ElasticClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+"/_cluster/health", nil)
	if err != nil {
		return err
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("elasticsearch cluster health falhou (%s): %s", resp.Status, string(b))
	}
	return nil
}

func (e *ElasticClient) EnsureIndex(ctx context.Context) error {
	mapping := map[string]any{
		"mappings": map[string]any{
			"properties": map[string]any{
				"keyword":      map[string]any{"type": "keyword"},
				"repoFullName": map[string]any{"type": "keyword"},
				"repoUrl":      map[string]any{"type": "keyword"},
				"filePath":     map[string]any{"type": "text"},
				"fileUrl":      map[string]any{"type": "keyword"},
				"private":      map[string]any{"type": "boolean"},
				"indexedAt":    map[string]any{"type": "date"},
			},
		},
	}
	body, _ := json.Marshal(mapping)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, e.baseURL+"/"+hitsIndex, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 400 normalmente significa "índice já existe" — ok.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ensure index falhou (%s): %s", resp.Status, string(b))
	}
	return nil
}

type Hit struct {
	Keyword         string     `json:"keyword"`
	RepoFullName    string     `json:"repoFullName"`
	RepoURL         string     `json:"repoUrl"`
	FilePath        string     `json:"filePath"`
	FileURL         string     `json:"fileUrl"`
	Private         bool       `json:"private"`
	HasCandidate    bool       `json:"hasCandidate"`             // achou um VALOR (não só o nome da variável) no conteúdo real
	MatchedValue    string     `json:"matchedValue"`             // valor completo capturado (sem máscara)
	GuessedProvider string     `json:"guessedProvider"`          // provedor sugerido pelo prefixo (só reconhecimento de padrão, sem chamada de rede)
	VerifierFamily  string     `json:"verifierFamily"`           // family usada pra chamar o verificador — guardada pra revalidar depois sem redescobrir
	OwnRepo         bool       `json:"ownRepo"`                  // repo está na allowlist (verificação ativa permitida)
	LiveChecked     bool       `json:"liveChecked"`              // chegamos a chamar a API real do provedor
	LiveActive      bool       `json:"liveActive"`               // a API confirmou que a chave ainda funciona (200)
	LiveSandbox     bool       `json:"liveSandbox"`              // confirmado ativa, mas é credencial de teste/sandbox (não move dado/dinheiro real)
	LastVerifiedAt  *time.Time `json:"lastVerifiedAt,omitempty"` // última vez que a verificação ativa rodou de verdade
	IndexedAt       time.Time  `json:"indexedAt"`
}

func docID(repoFullName, filePath, keyword string) string {
	h := sha1.Sum([]byte(repoFullName + "|" + filePath + "|" + keyword))
	return hex.EncodeToString(h[:])
}

// IndexHit usa PUT com ID determinístico (hash de repo+path+keyword), então
// rodar o mesmo scan de novo não duplica documentos — funciona como cache.
func (e *ElasticClient) IndexHit(ctx context.Context, hit Hit) error {
	body, _ := json.Marshal(hit)
	id := docID(hit.RepoFullName, hit.FilePath, hit.Keyword)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/%s/_doc/%s", e.baseURL, hitsIndex, id), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("index hit falhou (%s): %s", resp.Status, string(b))
	}
	return nil
}

func (e *ElasticClient) CountHits(ctx context.Context) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+"/"+hitsIndex+"/_count", nil)
	resp, err := e.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("count hits falhou (%s): %s", resp.Status, string(b))
	}
	var out struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (e *ElasticClient) ListHits(ctx context.Context, keyword string, onlyCandidates bool, size int) ([]Hit, error) {
	var filters []any
	if keyword != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"keyword": keyword}})
	}
	if onlyCandidates {
		// só valor real encontrado — esconde os "candidato" (nome da
		// variável sem valor atribuído), que é ruído na maioria dos casos.
		filters = append(filters, map[string]any{"term": map[string]any{"hasCandidate": true}})
	}

	query := map[string]any{
		"size": size,
		// liveActive primeiro: com o índice crescendo sem parar (scan
		// continua achando coisas novas), um corte fixo de `size` ordenado só
		// por data empurra achados antigos pra fora da janela visível —
		// inclusive os que estão ATIVOS agora. Ordenando por liveActive desc
		// primeiro garante que uma chave ativa nunca some da lista só porque
		// apareceu achado novo e irrelevante.
		"sort": []any{
			map[string]any{"liveActive": "desc"},
			map[string]any{"indexedAt": "desc"},
		},
	}
	if len(filters) == 0 {
		query["query"] = map[string]any{"match_all": map[string]any{}}
	} else {
		query["query"] = map[string]any{"bool": map[string]any{"filter": filters}}
	}
	body, _ := json.Marshal(query)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/"+hitsIndex+"/_search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list hits falhou (%s): %s", resp.Status, string(b))
	}

	var out struct {
		Hits struct {
			Hits []struct {
				Source Hit `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Hits.Hits))
	for _, h := range out.Hits.Hits {
		hits = append(hits, h.Source)
	}
	return hits, nil
}

// ListVerifiableHits busca TODOS os hits com valor real capturado — é a
// base pra revalidação periódica, não só a página visível na UI. Sem
// paginação/scroll: até 10.000 hits (limite padrão do ES) cobre o uso real
// dessa ferramenta com folga, e evita a complicação de search_after (que
// exige um tiebreaker sortável — "_id" é rejeitado por padrão pelo ES 8.x).
func (e *ElasticClient) ListVerifiableHits(ctx context.Context) ([]Hit, error) {
	query := map[string]any{
		"size":  10000,
		"query": map[string]any{"bool": map[string]any{"filter": []any{map[string]any{"term": map[string]any{"hasCandidate": true}}}}},
	}
	body, _ := json.Marshal(query)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/"+hitsIndex+"/_search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list verifiable hits falhou (%s): %s", resp.Status, string(b))
	}

	var out struct {
		Hits struct {
			Hits []struct {
				Source Hit `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Hits.Hits))
	for _, h := range out.Hits.Hits {
		hits = append(hits, h.Source)
	}
	return hits, nil
}

// KeywordStats agrega quantos hits cada keyword rendeu — é a "lista pra ver
// quais palavras-chave focar".
func (e *ElasticClient) KeywordStats(ctx context.Context) (map[string]int, error) {
	query := map[string]any{
		"size": 0,
		"aggs": map[string]any{
			"by_keyword": map[string]any{
				"terms": map[string]any{"field": "keyword", "size": 50},
			},
		},
	}
	body, _ := json.Marshal(query)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/"+hitsIndex+"/_search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("keyword stats falhou (%s): %s", resp.Status, string(b))
	}
	var out struct {
		Aggregations struct {
			ByKeyword struct {
				Buckets []struct {
					Key      string `json:"key"`
					DocCount int    `json:"doc_count"`
				} `json:"buckets"`
			} `json:"by_keyword"`
		} `json:"aggregations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	stats := make(map[string]int)
	for _, b := range out.Aggregations.ByKeyword.Buckets {
		stats[b.Key] = b.DocCount
	}
	return stats, nil
}

func (e *ElasticClient) ClearIndex(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, e.baseURL+"/"+hitsIndex, nil)
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
