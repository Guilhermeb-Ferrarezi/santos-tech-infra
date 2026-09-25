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
	url     string
	secret  string
	http    *http.Client
	sitemap *SitemapCache
	notion  *NotionClient
	// turmas — turmas ao vivo da plataforma (nil = sem PLATFORM_API_TOKEN).
	turmas *TurmasFonte

	// modelo — qual Claude atende o cliente.
	//
	// Era "opus" chumbado no código. A conversa do bot é seguir regras e devolver
	// um JSON: Sonnet dá conta, por uma fração do preço, e o prompt é grande
	// (a base de conhecimento inteira vai em toda mensagem), então a diferença
	// pesa em cada atendimento.
	//
	// Configurável por ambiente para dar para trocar sem deploy: se a qualidade
	// cair em algo — resolver "quinta que vem" na data certa é o mais sensível —
	// volta-se a opus mexendo numa variável.
	modelo string
}

// NewAgentGoClient cria um cliente com timeout de 120s (adequado para inferência).
// sitemap fornece as URLs do site (WebFetch); notion fornece a agenda de aulas.
func NewAgentGoClient(url, secret, modelo string, sitemap *SitemapCache, notion *NotionClient) *AgentGoClient {
	if modelo == "" {
		modelo = "sonnet"
	}
	return &AgentGoClient{
		url:     url,
		secret:  secret,
		http:    &http.Client{Timeout: 120 * time.Second},
		sitemap: sitemap,
		notion:  notion,
		modelo:  modelo,
	}
}

// agentGoRequest é o body de POST /claude/generate.
type agentGoRequest struct {
	Task  string `json:"task"`
	Brief string `json:"brief"`
	Model string `json:"model"`
	Web   bool   `json:"web"`
}

// agentGoResponse é a resposta de POST /claude/generate.
type agentGoResponse struct {
	Text      string     `json:"text"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
}

// Respond implementa a interface Responder do engine.
// Monta o prompt completo, envia ao agent-go e parseia a resposta.
func (c *AgentGoClient) Respond(ctx context.Context, conv Conversation, convCtx ConversationContext, cfg TenantConfig, inboundText string) (ResponderOutput, error) {
	// Páginas que o bot pode consultar via WebFetch vêm do sitemap.xml do site.
	if c.sitemap != nil {
		cfg.AllowedWebURLs = c.sitemap.URLs(ctx)
	}
	// Agenda de aulas (Notion) para o bot propor horários de agendamento.
	if c.notion != nil && !cfg.IsAdminConversation {
		cfg.Schedule, cfg.EstadoAgenda = c.notion.Schedule(ctx)
	}
	// Turmas da plataforma (ou o retrato, se a consulta falhar).
	if c.turmas != nil && !cfg.IsAdminConversation {
		cfg.Turmas = c.turmas.Estado(ctx, cfg.TenantID, horaOuPadrao(cfg.EscolaAbre, "08:00"), horaOuPadrao(cfg.EscolaFecha, "22:00"))
	}
	prompt := BuildPrompt(cfg, convCtx, inboundText, time.Now())

	result, err := c.callAPI(ctx, agentGoRequest{Task: "raw", Brief: prompt, Model: c.modelo, Web: true})
	if err != nil {
		return ResponderOutput{}, fmt.Errorf("agent_go: respond: %w", err)
	}

	out, err := ParseModelReply(result.Text)
	if err != nil {
		return ResponderOutput{}, fmt.Errorf("agent_go: parse reply: %w", err)
	}
	out.ToolCalls = result.ToolCalls

	return out, nil
}

// RespondWithModel envia um prompt ao agent-go com o modelo especificado e retorna o texto bruto.
// Usado por classificadores (ex: haiku) e pelo Respond principal.
// useWeb habilita WebSearch/WebFetch no Claude para esta geração.
func (c *AgentGoClient) RespondWithModel(ctx context.Context, prompt, model string, useWeb bool) (string, error) {
	result, err := c.callAPI(ctx, agentGoRequest{Task: "raw", Brief: prompt, Model: model, Web: useWeb})
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// callAPI faz a requisição HTTP ao agent-go e retorna a resposta completa.
func (c *AgentGoClient) callAPI(ctx context.Context, body agentGoRequest) (agentGoResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return agentGoResponse{}, fmt.Errorf("agent_go: marshal request: %w", err)
	}

	url := c.url + "/claude/generate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return agentGoResponse{}, fmt.Errorf("agent_go: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return agentGoResponse{}, fmt.Errorf("agent_go: http do: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return agentGoResponse{}, fmt.Errorf("agent_go: status %d: %s", resp.StatusCode, string(raw))
	}

	var result agentGoResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return agentGoResponse{}, fmt.Errorf("agent_go: unmarshal response: %w", err)
	}
	if result.Text == "" {
		return agentGoResponse{}, fmt.Errorf("agent_go: resposta vazia do servidor")
	}

	return result, nil
}

// ComTurmas liga a consulta de turmas ao vivo ao cliente. nil desliga.
func (c *AgentGoClient) ComTurmas(f *TurmasFonte) *AgentGoClient {
	c.turmas = f
	return c
}

// horaOuPadrao: o funcionamento vem do ambiente; vazio vira o padrão, porque a
// consulta de horários livres recusa parâmetro vazio.
func horaOuPadrao(h, padrao string) string {
	if h == "" {
		return padrao
	}
	return h
}
