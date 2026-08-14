package main

import (
	"context"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools do bot de atendimento WhatsApp (api.santos-tech.com/bot). Diferente das
// demais, estas autenticam no destino com a DASH key de serviço (X-Dash-Key) via
// proxyBot — o dashboard do bot não usa o token do usuário. São todas read-only.

type conversationsListInput struct {
	ID string `json:"id,omitempty" jsonschema:"id (uuid) da conversa; se informado, devolve o histórico de mensagens dela em vez da lista"`
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
		Name: "conversations_list",
		Description: "Sem id: lista as conversas de WhatsApp do atendimento (contato, último contato, estado do bot). " +
			"Com id: histórico de mensagens dessa conversa.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in conversationsListInput) (*mcp.CallToolResult, any, error) {
		if in.ID != "" {
			return s.proxyBot(ctx, "GET", base+"/api/conversations/"+url.PathEscape(in.ID)+"/messages", nil)
		}
		return s.proxyBot(ctx, "GET", base+"/api/conversations", nil)
	})
}
