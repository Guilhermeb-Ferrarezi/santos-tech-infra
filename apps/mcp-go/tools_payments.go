package main

import (
	"context"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools da API de pagamentos (payments-go). Todas exigem role Admin (a API valida).

type chargeListInput struct {
	Status    string `json:"status,omitempty" jsonschema:"filtro de status: pending, paid, canceled, expired"`
	StudentID int64  `json:"studentId,omitempty" jsonschema:"filtrar por id do aluno"`
}

type chargeIDInput struct {
	ID int64 `json:"id" jsonschema:"id da cobrança"`
}

type analyticsRangeInput struct {
	Range string `json:"range,omitempty" jsonschema:"janela de tempo: 7d (padrão), 30d, 90d"`
}

type efiMEDInput struct {
	Range string `json:"range,omitempty" jsonschema:"janela de tempo: 7d (padrão), 30d, 90d"`
}

func (s *Server) addPaymentsTools(srv *mcp.Server) {
	base := s.cfg.PaymentsAPIURL

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "payments_stats",
		Description: "Estatísticas de pagamentos (admin): total de cobranças, alunos, receita e inadimplência.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/stats", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "payments_analytics",
		Description: "Série temporal de pagamentos (admin): cobranças geradas e recebidas por dia. range: 7d (padrão), 30d, 90d.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in analyticsRangeInput) (*mcp.CallToolResult, any, error) {
		u := base + "/analytics"
		if in.Range != "" {
			u += "?range=" + url.QueryEscape(in.Range)
		}
		return s.proxy(ctx, req, "GET", u, nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "charges_list",
		Description: "Lista cobranças PIX (admin). Filtre por status (pending/paid/canceled/expired) e/ou id do aluno.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in chargeListInput) (*mcp.CallToolResult, any, error) {
		u := base + "/charges"
		sep := "?"
		if in.Status != "" {
			u += sep + "status=" + url.QueryEscape(in.Status)
			sep = "&"
		}
		if in.StudentID > 0 {
			u += sep + fmt.Sprintf("student_id=%d", in.StudentID)
		}
		return s.proxy(ctx, req, "GET", u, nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "charges_get",
		Description: "Detalha uma cobrança PIX pelo id (admin): status, QR code, aluno, data de vencimento.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in chargeIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", fmt.Sprintf("%s/charges/%d", base, in.ID), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "subscriptions_list",
		Description: "Lista assinaturas de mensalidade (admin): aluno, plano, status, dia de vencimento.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/subscriptions", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "recurrences_list",
		Description: "Lista contratos de PIX Automático — recorrência BACEN (admin): status, aluno, valor, ciclos.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/recurrences", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "efi_balance",
		Description: "Saldo disponível na conta PIX da Efí (admin), em centavos.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/efi/balance", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "efi_med",
		Description: "Lista infrações MED (disputas e devoluções PIX) na Efí. range: 7d (padrão), 30d, 90d.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in efiMEDInput) (*mcp.CallToolResult, any, error) {
		u := base + "/efi/med"
		if in.Range != "" {
			u += "?range=" + url.QueryEscape(in.Range)
		}
		return s.proxy(ctx, req, "GET", u, nil)
	})
}
