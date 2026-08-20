package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Presets de keywords: listas nomeadas e reutilizáveis, salvas à parte da
// lista "ativa" do scan (StateData.Keywords). Resolve o fluxo de "só
// colocou e tirou" — antes, toda keyword digitada ou colada em bulk ia
// direto pra lista ativa, e uma vez removida de lá não sobrava em lugar
// nenhum. Agora dá pra salvar um conjunto (ex. "Pagamentos BR") e reaplicar
// depois sem redigitar/recolar.
const presetsIndex = "secrets-keyword-presets"

type KeywordPreset struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Keywords  []string  `json:"keywords"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (e *ElasticClient) EnsurePresetsIndex(ctx context.Context) error {
	mapping := map[string]any{
		"mappings": map[string]any{
			"properties": map[string]any{
				"name":      map[string]any{"type": "keyword"},
				"keywords":  map[string]any{"type": "keyword"},
				"createdAt": map[string]any{"type": "date"},
				"updatedAt": map[string]any{"type": "date"},
			},
		},
	}
	body, _ := json.Marshal(mapping)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, e.baseURL+"/"+presetsIndex, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 400 normalmente significa "índice já existe" — ok.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ensure presets index falhou (%s): %s", resp.Status, string(b))
	}
	return nil
}

func (e *ElasticClient) ListPresets(ctx context.Context) ([]KeywordPreset, error) {
	query := map[string]any{
		"size":  200,
		"query": map[string]any{"match_all": map[string]any{}},
		"sort":  []any{map[string]any{"name": "asc"}},
	}
	body, _ := json.Marshal(query)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/"+presetsIndex+"/_search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// índice pode não existir ainda (nenhum preset criado) — lista vazia, não erro.
	if resp.StatusCode == http.StatusNotFound {
		return []KeywordPreset{}, nil
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list presets falhou (%s): %s", resp.Status, string(b))
	}
	var out struct {
		Hits struct {
			Hits []struct {
				ID     string        `json:"_id"`
				Source KeywordPreset `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	presets := make([]KeywordPreset, 0, len(out.Hits.Hits))
	for _, h := range out.Hits.Hits {
		p := h.Source
		p.ID = h.ID
		presets = append(presets, p)
	}
	return presets, nil
}

func (e *ElasticClient) PutPreset(ctx context.Context, p KeywordPreset) error {
	if !validPresetID(p.ID) {
		return errInvalidPresetID
	}
	body, _ := json.Marshal(p)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/%s/_doc/%s", e.baseURL, presetsIndex, url.PathEscape(p.ID)), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put preset falhou (%s): %s", resp.Status, string(b))
	}
	return nil
}

func (e *ElasticClient) DeletePreset(ctx context.Context, id string) error {
	if !validPresetID(id) {
		return errInvalidPresetID
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/%s/_doc/%s", e.baseURL, presetsIndex, url.PathEscape(id)), nil)
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete preset falhou (%s): %s", resp.Status, string(b))
	}
	return nil
}

func newPresetID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// errInvalidPresetID barra id fora do formato gerado por newPresetID.
var errInvalidPresetID = errors.New("id de preset inválido")

// validPresetID exige exatamente o que newPresetID produz: 24 caracteres hex
// minúsculos.
//
// O id vem de r.PathValue("id") e era interpolado cru na URL do Elasticsearch.
// Um DELETE em /keyword-presets/x%3Frefresh%3Dtrue injetava query string na
// chamada ao cluster, e com %2F dava para alcançar outro caminho da API de um
// cluster que é compartilhado. Validar o formato (mais url.PathEscape como
// segunda barreira) fecha os dois.
func validPresetID(id string) bool {
	if len(id) != 24 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
