package main

import (
	"context"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools do bot de atendimento WhatsApp (api.santos-tech.com/bot). Diferente das
// demais, estas autenticam no destino com a DASH key de serviço (X-Dash-Key) via
// proxyBot — o dashboard do bot não usa o token do usuário. São todas read-only.

type conversationIDInput struct {
	ID string `json:"id" jsonschema:"id (uuid) da conversa, obtido em conversations_list"`
}

func (s *Server) addBotTools(srv *mcp.Server) {
	base := s.cfg.BotAPIURL

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bookings_list",
		Description: "Lista as aulas experimentais agendadas (agenda do Notion) e os agendamentos aguardando confirmação do admin. Use para conferir se um agendamento foi registrado.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxyBot(ctx, "GET", base+"/api/bookings", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "leads_list",
		Description: "Lista os leads capturados no atendimento do WhatsApp (nome, telefone, status no funil).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxyBot(ctx, "GET", base+"/api/leads", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "conversations_list",
		Description: "Lista as conversas de WhatsApp do atendimento (contato, último contato, estado do bot).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxyBot(ctx, "GET", base+"/api/conversations", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "conversation_messages",
		Description: "Lê o histórico de mensagens de uma conversa de WhatsApp pelo id (use conversations_list para obter o id).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in conversationIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxyBot(ctx, "GET", base+"/api/conversations/"+url.PathEscape(in.ID)+"/messages", nil)
	})
}
