package main

import (
	"context"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools do bot de atendimento WhatsApp (api.santos-tech.com/bot). Diferente das
// demais, estas autenticam no destino com a DASH key de serviço (X-Dash-Key) via
// proxyBot — o dashboard do bot não usa o token do usuário. Todas exigem papel
// Admin (ver proxyBot): a credencial é do serviço, então o papel de quem chamou
// precisa ser checado aqui, não no destino.
//
// bookings_list, leads_list e conversations_list são read-only. bookings_confirm,
// bookings_reject e bookings_reschedule GRAVAM na agenda real (Notion) ou no
// banco de pendências do bot-go — o próprio bot-go valida (conflito de horário,
// status da pendência) antes de escrever; este proxy nunca fala com o Notion
// diretamente.

type conversationsListInput struct {
	ID string `json:"id,omitempty" jsonschema:"id (uuid) da conversa; se informado, devolve o histórico de mensagens dela em vez da lista"`
}

type bookingConfirmInput struct {
	ID   string `json:"id" jsonschema:"id (uuid) do agendamento pendente — ver bookings_list, campo pending[].id"`
	Day  string `json:"day,omitempty" jsonschema:"opcional: sobrescreve o dia proposto pelo lead (ex.: quinta, 2026-09-25, 25/09)"`
	Time string `json:"time,omitempty" jsonschema:"opcional: sobrescreve o horário proposto (ex.: 09:00)"`
}

type bookingRejectInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do agendamento pendente — ver bookings_list, campo pending[].id"`
}

type bookingRescheduleInput struct {
	Source  string `json:"source" jsonschema:"'notion' (aula já na grade, use o pageId de bookings_list) ou 'pending' (agendamento ainda pendente)"`
	ID      string `json:"id" jsonschema:"pageId (source=notion) ou id do pendente (source=pending)"`
	Date    string `json:"date" jsonschema:"nova data, YYYY-MM-DD"`
	Time    string `json:"time" jsonschema:"novo horário, HH:MM"`
	Phone   string `json:"phone,omitempty" jsonschema:"telefone do aluno — usado para achar a conversa e avisar por WhatsApp"`
	Message string `json:"message,omitempty" jsonschema:"mensagem opcional avisando o aluno da remarcação pelo WhatsApp"`
}

func (s *Server) addBotTools(srv *mcp.Server) {
	base := s.cfg.BotAPIURL

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bookings_list",
		Description: "Lista as aulas experimentais agendadas (agenda do Notion) e os agendamentos aguardando confirmação do admin. Use para conferir se um agendamento foi registrado.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxyBot(ctx, req, "GET", base+"/api/bookings", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "leads_list",
		Description: "Lista os leads capturados no atendimento do WhatsApp (nome, telefone, status no funil).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxyBotUntrusted(ctx, req, "GET", base+"/api/leads", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "conversations_list",
		Description: "Sem id: lista as conversas de WhatsApp do atendimento (contato, último contato, estado do bot). " +
			"Com id: histórico de mensagens dessa conversa.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in conversationsListInput) (*mcp.CallToolResult, any, error) {
		if in.ID != "" {
			return s.proxyBotUntrusted(ctx, req, "GET", base+"/api/conversations/"+url.PathEscape(in.ID)+"/messages", nil)
		}
		return s.proxyBotUntrusted(ctx, req, "GET", base+"/api/conversations", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "bookings_confirm",
		Description: "Confirma um agendamento pendente (lead que pediu aula experimental pelo WhatsApp): grava a aula na Agenda de Aulas do Notion e marca a pendência como confirmada. " +
			"Recusa automaticamente (409) se o horário já estiver ocupado por outra aula.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in bookingConfirmInput) (*mcp.CallToolResult, any, error) {
		return s.proxyBot(ctx, req, "POST", base+"/api/bookings/confirm", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bookings_reject",
		Description: "Recusa um agendamento pendente sem gravar nada no Notion.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in bookingRejectInput) (*mcp.CallToolResult, any, error) {
		return s.proxyBot(ctx, req, "POST", base+"/api/bookings/"+url.PathEscape(in.ID)+"/reject", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "bookings_reschedule",
		Description: "Remarca para nova data/hora uma aula já na grade (source=notion) ou um agendamento pendente (source=pending). " +
			"Opcionalmente avisa o aluno por WhatsApp (message + phone).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in bookingRescheduleInput) (*mcp.CallToolResult, any, error) {
		return s.proxyBot(ctx, req, "POST", base+"/api/bookings/reschedule", in)
	})
}
