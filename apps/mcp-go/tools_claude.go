package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools do Claude Agent (api.santos-tech.com/claude).

type agentGenerateInput struct {
	Task     string `json:"task,omitempty" jsonschema:"tipo de geração: email | rewrite | subjects | spamcheck (padrão: email)"`
	Brief    string `json:"brief" jsonschema:"descrição do que gerar / instrução de reescrita"`
	Tone     string `json:"tone,omitempty" jsonschema:"tom desejado"`
	Audience string `json:"audience,omitempty" jsonschema:"público-alvo"`
	Subject  string `json:"subject,omitempty" jsonschema:"assunto a analisar (spamcheck)"`
	HTML     string `json:"html,omitempty" jsonschema:"conteúdo atual (obrigatório em rewrite/spamcheck)"`
}

func (s *Server) addClaudeTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "agent_generate",
		Description: "Geração one-shot pelo Claude Agent: conteúdo de email, reescrita, sugestões de assunto ou análise de spam.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in agentGenerateInput) (*mcp.CallToolResult, any, error) {
		// Geração pode levar até ~90s no agent — prazo maior que o padrão.
		return s.proxyTimeout(ctx, req, "POST", s.cfg.ClaudeBaseURL+"/claude/generate", in, generateCallTimeout)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "agent_conversations",
		Description: "Lista as conversas do usuário no Claude Agent.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", s.cfg.ClaudeBaseURL+"/claude/conversations", nil)
	})
}
