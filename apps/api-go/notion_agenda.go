package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// notionAgendaVersion — API com data sources (database multi-source), a mesma
// versão usada pelo cliente do bot-go.
const notionAgendaVersion = "2025-09-03"

// notionAulaRow é uma linha da base "Agenda de Aulas" do Notion, já com os
// tipos do Notion (title/select/multi_select/rich_text) achatados em Go.
type notionAulaRow struct {
	PageID    string
	Aula      string   // title — às vezes é o nome do aluno, às vezes "Turma X"
	Aluno     []string // multi_select — uma opção pode juntar VÁRIOS alunos
	Dia       string   // select — Segunda..Sábado
	Horario   string   // texto livre — 3 formatos, ver notionParseHorario
	Professor string   // select
	Conteudo  string   // select — vira o curso
}

// notionAgendaClient lê a base "Agenda de Aulas". Só leitura: esta integração
// nunca escreve no Notion, pra não haver caminho de volta que sobrescreva o que
// alguém organizou lá.
type notionAgendaClient struct {
	token  string
	dsID   string
	client *http.Client
}

func newNotionAgendaClient(cfg Config) *notionAgendaClient {
	if cfg.NotionToken == "" || cfg.NotionAgendaDSID == "" {
		return nil
	}
	return &notionAgendaClient{
		token:  cfg.NotionToken,
		dsID:   cfg.NotionAgendaDSID,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *notionAgendaClient) enabled() bool { return c != nil }

// fetchRows pagina a base inteira. O teto de 20 páginas é uma trava de
// segurança: a base tem dezenas de linhas, não milhares.
func (c *notionAgendaClient) fetchRows(ctx context.Context) ([]notionAulaRow, error) {
	var out []notionAulaRow
	cursor := ""
	for page := 0; page < 20; page++ {
		body := map[string]any{"page_size": 100}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		raw, _ := json.Marshal(body)
		url := fmt.Sprintf("https://api.notion.com/v1/data_sources/%s/query", c.dsID)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("criar request do notion: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Notion-Version", notionAgendaVersion)
		req.Header.Set("Content-Type", "application/json")

		res, err := c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("chamar notion: %w", err)
		}
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		res.Body.Close()
		if res.StatusCode >= 300 {
			return nil, fmt.Errorf("notion retornou status %d: %s", res.StatusCode, payload)
		}
		var parsed struct {
			Results []struct {
				ID         string                     `json:"id"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"results"`
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(payload, &parsed); err != nil {
			return nil, fmt.Errorf("decodificar resposta do notion: %w", err)
		}
		for _, r := range parsed.Results {
			out = append(out, notionAulaRow{
				PageID:    r.ID,
				Aula:      notionPlainTitle(r.Properties["Aula"]),
				Aluno:     notionMultiSelect(r.Properties["Aluno"]),
				Dia:       notionSelectName(r.Properties["Dia"]),
				Horario:   notionPlainRichText(r.Properties["Horário"]),
				Professor: notionSelectName(r.Properties["Professor"]),
				Conteudo:  notionSelectName(r.Properties["Conteúdo"]),
			})
		}
		if !parsed.HasMore || parsed.NextCursor == "" {
			break
		}
		cursor = parsed.NextCursor
	}
	return out, nil
}

func notionPlainTitle(raw json.RawMessage) string {
	var v struct {
		Title []struct {
			PlainText string `json:"plain_text"`
		} `json:"title"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	s := ""
	for _, t := range v.Title {
		s += t.PlainText
	}
	return s
}

func notionPlainRichText(raw json.RawMessage) string {
	var v struct {
		RichText []struct {
			PlainText string `json:"plain_text"`
		} `json:"rich_text"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	s := ""
	for _, t := range v.RichText {
		s += t.PlainText
	}
	return s
}

func notionSelectName(raw json.RawMessage) string {
	var v struct {
		Select *struct {
			Name string `json:"name"`
		} `json:"select"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Select == nil {
		return ""
	}
	return v.Select.Name
}

func notionMultiSelect(raw json.RawMessage) []string {
	var v struct {
		MultiSelect []struct {
			Name string `json:"name"`
		} `json:"multi_select"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	out := make([]string, 0, len(v.MultiSelect))
	for _, o := range v.MultiSelect {
		out = append(out, o.Name)
	}
	return out
}
