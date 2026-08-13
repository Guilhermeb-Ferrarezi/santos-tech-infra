package main

// Adaptadores de chat do roteador de APIs: sabem montar a requisição nativa
// de cada família de provider a partir de um prompt normalizado, e extrair o
// texto da resposta nativa de volta. "openai_compatible" cobre a maioria dos
// provedores famosos (OpenAI, Mistral, DeepSeek, xAI/Grok — todos falam o
// dialeto /v1/chat/completions da OpenAI); Anthropic e Google Gemini têm
// formato de request/response próprio.

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	chatAdapterOpenAICompatible = "openai_compatible"
	chatAdapterAnthropic        = "anthropic"
	chatAdapterGoogle           = "google"
)

// validChatAdapters é a allowlist aceita em provider.chatAdapter (create/update).
var validChatAdapters = map[string]bool{
	chatAdapterOpenAICompatible: true,
	chatAdapterAnthropic:        true,
	chatAdapterGoogle:           true,
}

// buildChatRequest monta path/body/headers no formato nativo do provider a
// partir de um prompt normalizado. model vazio usa um default razoável do
// adapter — o chamador pode sempre informar um modelo específico.
func buildChatRequest(adapter, model, prompt string) (path string, body []byte, headers map[string]string, err error) {
	switch adapter {
	case chatAdapterAnthropic:
		if model == "" {
			model = "claude-sonnet-4-5"
		}
		body, err = json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 1024,
			"messages":   []map[string]string{{"role": "user", "content": prompt}},
		})
		if err != nil {
			return "", nil, nil, fmt.Errorf("apirouter: montar corpo anthropic: %w", err)
		}
		// anthropic-version é obrigatório em toda chamada à API da Anthropic.
		return "/v1/messages", body, map[string]string{"anthropic-version": "2023-06-01"}, nil

	case chatAdapterGoogle:
		if model == "" {
			model = "gemini-2.5-flash"
		}
		body, err = json.Marshal(map[string]any{
			"contents": []map[string]any{{"parts": []map[string]string{{"text": prompt}}}},
		})
		if err != nil {
			return "", nil, nil, fmt.Errorf("apirouter: montar corpo google: %w", err)
		}
		// Gemini carrega o modelo no PATH, não no corpo.
		return fmt.Sprintf("/v1beta/models/%s:generateContent", model), body, nil, nil

	case chatAdapterOpenAICompatible, "":
		if model == "" {
			model = "gpt-4o-mini"
		}
		body, err = json.Marshal(map[string]any{
			"model":    model,
			"messages": []map[string]string{{"role": "user", "content": prompt}},
		})
		if err != nil {
			return "", nil, nil, fmt.Errorf("apirouter: montar corpo openai-compatible: %w", err)
		}
		return "/v1/chat/completions", body, nil, nil

	default:
		return "", nil, nil, fmt.Errorf("apirouter: adapter desconhecido %q", adapter)
	}
}

// parseChatResponse extrai o texto de resposta do corpo nativo do provider.
// Se o provider devolveu um erro no formato dele, devolve esse erro (não um
// texto vazio) — é assim que uma requisição malformada (ex.: modelo errado)
// fica visível pro chamador em vez de "".
func parseChatResponse(adapter string, raw []byte) (string, error) {
	switch adapter {
	case chatAdapterAnthropic:
		var resp struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", fmt.Errorf("apirouter: parse anthropic: %w", err)
		}
		if resp.Error != nil {
			return "", fmt.Errorf("anthropic: %s", resp.Error.Message)
		}
		if len(resp.Content) == 0 {
			return "", fmt.Errorf("apirouter: resposta anthropic sem conteúdo")
		}
		return resp.Content[0].Text, nil

	case chatAdapterGoogle:
		var resp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", fmt.Errorf("apirouter: parse google: %w", err)
		}
		if resp.Error != nil {
			return "", fmt.Errorf("google: %s", resp.Error.Message)
		}
		if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
			return "", fmt.Errorf("apirouter: resposta google sem conteúdo")
		}
		var sb strings.Builder
		for _, p := range resp.Candidates[0].Content.Parts {
			sb.WriteString(p.Text)
		}
		return sb.String(), nil

	case chatAdapterOpenAICompatible, "":
		var resp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", fmt.Errorf("apirouter: parse openai-compatible: %w", err)
		}
		if resp.Error != nil {
			return "", fmt.Errorf("provider: %s", resp.Error.Message)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("apirouter: resposta sem choices")
		}
		return resp.Choices[0].Message.Content, nil

	default:
		return "", fmt.Errorf("apirouter: adapter desconhecido %q", adapter)
	}
}
