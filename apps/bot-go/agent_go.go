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

// AgentGoClient é o cliente HTTP para o agent-go (api.santos-tech.com).
type AgentGoClient struct {
	url    string
	secret string
	http   *http.Client
}

// NewAgentGoClient cria um cliente com timeout de 120s (adequado para inferência).
func NewAgentGoClient(url, secret string) *AgentGoClient {
	return &AgentGoClient{
		url:    url,
		secret: secret,
		http:   &http.Client{Timeout: 120 * time.Second},
	}
}

// agentGoRequest é o body de POST /claude/generate.
type agentGoRequest struct {
	Task  string `json:"task"`
	Brief string `json:"brief"`
	Model string `json:"model"`
}

// agentGoResponse é a resposta de POST /claude/generate.
type agentGoResponse struct {
	Text string `json:"text"`
}

// Respond implementa a interface Responder do engine.
// Monta o prompt completo, envia ao agent-go e parseia a resposta.
func (c *AgentGoClient) Respond(ctx context.Context, conv Conversation, convCtx ConversationContext, cfg TenantConfig, inboundText string) (ResponderOutput, error) {
	prompt := BuildPrompt(cfg, convCtx, inboundText, time.Now())

	raw, err := c.RespondWithModel(ctx, prompt, "opus")
	if err != nil {
		return ResponderOutput{}, fmt.Errorf("agent_go: respond: %w", err)
	}

	out, err := ParseModelReply(raw)
	if err != nil {
		return ResponderOutput{}, fmt.Errorf("agent_go: parse reply: %w", err)
	}

	return out, nil
}

// RespondWithModel envia um prompt ao agent-go com o modelo especificado e retorna o texto bruto.
// Usado por classificadores (ex: haiku) e pelo Respond principal.
func (c *AgentGoClient) RespondWithModel(ctx context.Context, prompt, model string) (string, error) {
	reqBody := agentGoRequest{
		Task:  "raw",
		Brief: prompt,
		Model: model,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("agent_go: marshal request: %w", err)
	}

	url := c.url + "/claude/generate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("agent_go: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent_go: http do: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agent_go: status %d: %s", resp.StatusCode, string(raw))
	}

	var result agentGoResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("agent_go: unmarshal response: %w", err)
	}
	if result.Text == "" {
		return "", fmt.Errorf("agent_go: resposta vazia do servidor")
	}

	return result.Text, nil
}
