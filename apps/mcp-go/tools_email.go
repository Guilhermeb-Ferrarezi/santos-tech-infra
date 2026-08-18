package main

import (
	"context"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools da API de email (mails.santos-tech.com). Caixa SEMPRE a própria
// (/mailbox/me): o From é forçado pela API ao dono do token, então um agente
// não consegue se passar por outra pessoa.

// mailboxReadInput: sem uid lista a caixa (limit se aplica); com uid lê uma
// mensagem específica (limit é ignorado).
type mailboxReadInput struct {
	UID   string `json:"uid,omitempty" jsonschema:"uid da mensagem; omitido lista a caixa"`
	Limit int    `json:"limit,omitempty" jsonschema:"lista: máximo de mensagens a retornar"`
}

type mailboxSendInput struct {
	To        string `json:"to" jsonschema:"destinatário"`
	Subject   string `json:"subject"`
	HTML      string `json:"html,omitempty" jsonschema:"corpo HTML (informe html ou text)"`
	Text      string `json:"text,omitempty" jsonschema:"corpo em texto puro (informe html ou text)"`
	InReplyTo string `json:"inReplyTo,omitempty" jsonschema:"Message-ID da mensagem sendo respondida"`
}

type emailLogsInput struct {
	Page int `json:"page,omitempty" jsonschema:"página (paginado)"`
}

func (s *Server) addEmailTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "mailbox_read",
		Description: "Sem uid: lista as mensagens da caixa de email do dono do token (conta @santos-tech.com). " +
			"Com uid: lê uma mensagem específica.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mailboxReadInput) (*mcp.CallToolResult, any, error) {
		if in.UID != "" {
			return s.proxy(ctx, req, "GET", s.cfg.EmailAPIURL+"/mailbox/me/messages/"+url.PathEscape(in.UID), nil)
		}
		u := s.cfg.EmailAPIURL + "/mailbox/me/messages"
		if in.Limit > 0 {
			u += fmt.Sprintf("?limit=%d", in.Limit)
		}
		return s.proxy(ctx, req, "GET", u, nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "mailbox_send",
		Description: "Envia um email pela caixa do dono do token (From forçado ao email da conta). Informe html ou text.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mailboxSendInput) (*mcp.CallToolResult, any, error) {
		if in.HTML == "" && in.Text == "" {
			return errResult("informe html ou text"), nil, nil
		}
		return s.proxy(ctx, req, "POST", s.cfg.EmailAPIURL+"/mailbox/me/send", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "email_metrics",
		Description: "Métricas de envio da plataforma de email (admin): entregas, aberturas, cliques.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", s.cfg.EmailAPIURL+"/metrics", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "email_logs",
		Description: "Logs de envio da plataforma de email (admin), paginados.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in emailLogsInput) (*mcp.CallToolResult, any, error) {
		u := s.cfg.EmailAPIURL + "/logs"
		if in.Page > 0 {
			u += fmt.Sprintf("?page=%d", in.Page)
		}
		return s.proxy(ctx, req, "GET", u, nil)
	})
}
