package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const hitsIndex = "dotfy-hits"

type ElasticClient struct {
	baseURL string
	http    *http.Client
	// vault cifra/decifra o matchedValue em repouso. nil = cofre desabilitado:
	// nesse caso o valor NÃO é gravado (só prefixo + hash), nunca em claro.
	vault *Vault
}

func NewElasticClient(baseURL string, vault *Vault) *ElasticClient {
	return &ElasticClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
		vault:   vault,
	}
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
				// Valor da chave: cifrado e explicitamente NÃO indexado. Sem
				// declarar, o mapeamento dinâmico fazia dele um "text"
				// pesquisável — era assim que o valor em claro virava
				// consultável por qualquer um com acesso ao cluster.
				"matchedValueEnc": map[string]any{"type": "keyword", "index": false},
				// Legado: documentos indexados antes da cifragem guardam o valor
				// em claro aqui. Declarado index:false para que num índice novo o
				// campo nunca seja pesquisável.
				"matchedValue":       map[string]any{"type": "keyword", "index": false},
				"matchedValuePrefix": map[string]any{"type": "keyword"},
				"matchedValueHash":   map[string]any{"type": "keyword"},
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
	ID              string     `json:"id,omitempty"` // _id do documento no ES (usado pelo POST /reveal)
	Keyword         string     `json:"keyword"`
	RepoFullName    string     `json:"repoFullName"`
	RepoURL         string     `json:"repoUrl"`
	FilePath        string     `json:"filePath"`
	FileURL         string     `json:"fileUrl"`
	Private         bool       `json:"private"`
	HasCandidate    bool       `json:"hasCandidate"`             // achou um VALOR (não só o nome da variável) no conteúdo real
	GuessedProvider string     `json:"guessedProvider"`          // provedor sugerido pelo prefixo (só reconhecimento de padrão, sem chamada de rede)
	VerifierFamily  string     `json:"verifierFamily"`           // family usada pra chamar o verificador — guardada pra revalidar depois sem redescobrir
	OwnRepo         bool       `json:"ownRepo"`                  // repo está na allowlist (verificação ativa permitida)
	LiveChecked     bool       `json:"liveChecked"`              // chegamos a chamar a API real do provedor
	LiveActive      bool       `json:"liveActive"`               // a API confirmou que a chave ainda funciona (200)
	LiveSandbox     bool       `json:"liveSandbox"`              // confirmado ativa, mas é credencial de teste/sandbox (não move dado/dinheiro real)
	LastVerifiedAt  *time.Time `json:"lastVerifiedAt,omitempty"` // última vez que a verificação ativa rodou de verdade
	IndexedAt       time.Time  `json:"indexedAt"`

	// MatchedValue é o valor completo da chave vazada. `json:"-"` de propósito:
	// NUNCA sai daqui serializado — não vai pro Elasticsearch, não vai pro SSE
	// (/events) e não vai pra listagem (/repos). Só dois caminhos dedicados o
	// expõem: POST /reveal (admin, uma chave por vez, com auditoria) e
	// /internal/sync-hits (service-to-service, token compartilhado).
	MatchedValue string `json:"-"`

	// Persistidos no lugar dele:
	MatchedValueEnc    string `json:"matchedValueEnc,omitempty"`    // AES-256-GCM (ver vault.go)
	MatchedValuePrefix string `json:"matchedValuePrefix,omitempty"` // primeiros caracteres — identifica o provedor na UI
	MatchedValueHash   string `json:"matchedValueHash,omitempty"`   // sha256 hex — correlaciona/deduplica sem revelar

	// MatchedValueLegacy lê os documentos indexados ANTES da cifragem, que têm o
	// valor em claro no campo "matchedValue". Só leitura: a escrita sempre o
	// deixa vazio (omitempty), então reindexar um hit antigo apaga o texto claro.
	MatchedValueLegacy string `json:"matchedValue,omitempty"`
}

// sealValue prepara o hit para gravação: calcula prefixo/hash, cifra o valor e
// garante que nem o texto claro nem o campo legado vão para o índice.
func (e *ElasticClient) sealValue(hit Hit, id string) (Hit, error) {
	hit.ID = ""                 // o id é a chave do documento, não conteúdo dele
	hit.MatchedValueLegacy = "" // nunca reescreve valor em claro
	if hit.MatchedValue == "" {
		hit.MatchedValueEnc, hit.MatchedValuePrefix, hit.MatchedValueHash = "", "", ""
		return hit, nil
	}
	hit.MatchedValuePrefix = valuePrefix(hit.MatchedValue)
	hit.MatchedValueHash = valueHash(hit.MatchedValue)
	enc, err := e.vault.Encrypt(hit.MatchedValue, hitAAD(id))
	if err != nil {
		// Sem cofre não existe "grava em claro": o valor simplesmente não é
		// persistido. O hit continua útil (prefixo, hash, provedor, status).
		hit.MatchedValueEnc = ""
		if errors.Is(err, errVaultDisabled) {
			return hit, nil
		}
		return hit, err
	}
	hit.MatchedValueEnc = enc
	return hit, nil
}

// openValue é o inverso: preenche MatchedValue a partir do ciphertext (ou do
// campo legado em claro, para documentos antigos ainda não recifrados).
func (e *ElasticClient) openValue(hit Hit, id string) Hit {
	hit.ID = id
	if hit.MatchedValueLegacy != "" {
		hit.MatchedValue = hit.MatchedValueLegacy
		hit.MatchedValueLegacy = ""
	} else if hit.MatchedValueEnc != "" {
		if v, err := e.vault.Decrypt(hit.MatchedValueEnc, hitAAD(id)); err == nil {
			hit.MatchedValue = v
		}
	}
	// Documento antigo sem fingerprint: deriva na hora para a UI não ficar vazia.
	if hit.MatchedValue != "" && hit.MatchedValuePrefix == "" {
		hit.MatchedValuePrefix = valuePrefix(hit.MatchedValue)
		hit.MatchedValueHash = valueHash(hit.MatchedValue)
	}
	return hit
}

func docID(repoFullName, filePath, keyword string) string {
	h := sha1.Sum([]byte(repoFullName + "|" + filePath + "|" + keyword))
	return hex.EncodeToString(h[:])
}

// decodeHits lê a resposta de _search e devolve os hits já com o matchedValue
// decifrado em memória (nunca serializado de volta — ver Hit).
func (e *ElasticClient) decodeHits(body io.Reader) ([]Hit, error) {
	var out struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source Hit    `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Hits.Hits))
	for _, h := range out.Hits.Hits {
		hits = append(hits, e.openValue(h.Source, h.ID))
	}
	return hits, nil
}

// errHitNotFound é devolvido por GetHit quando o documento não existe.
var errHitNotFound = errors.New("hit não encontrado")

// GetHit busca UM documento pelo _id. Usado só pelo POST /reveal — revelar o
// valor de uma chave por vez, nunca em lote.
func (e *ElasticClient) GetHit(ctx context.Context, id string) (Hit, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/%s/_doc/%s", e.baseURL, hitsIndex, url.PathEscape(id)), nil)
	resp, err := e.http.Do(req)
	if err != nil {
		return Hit{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Hit{}, errHitNotFound
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Hit{}, fmt.Errorf("get hit falhou (%s): %s", resp.Status, string(b))
	}
	var out struct {
		ID     string `json:"_id"`
		Found  bool   `json:"found"`
		Source Hit    `json:"_source"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Hit{}, err
	}
	if !out.Found {
		return Hit{}, errHitNotFound
	}
	return e.openValue(out.Source, out.ID), nil
}

// IndexHit usa PUT com ID determinístico (hash de repo+path+keyword), então
// rodar o mesmo scan de novo não duplica documentos — funciona como cache.
func (e *ElasticClient) IndexHit(ctx context.Context, hit Hit) error {
	id := docID(hit.RepoFullName, hit.FilePath, hit.Keyword)
	sealed, err := e.sealValue(hit, id)
	if err != nil {
		return fmt.Errorf("cifrando matchedValue: %w", err)
	}
	body, _ := json.Marshal(sealed)
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

	return e.decodeHits(resp.Body)
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

	return e.decodeHits(resp.Body)
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
