package main

import (
	"context"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools de administração de usuários (auth central). A API exige role 3 (admin)
// — quem decide é ela; aqui só repassamos o token.

type listUsersInput struct {
	Scope string `json:"scope,omitempty" jsonschema:"use \"all\" para listar usuários de todos os domínios; vazio lista só contas @santos-tech.com"`
}

type createUserInput struct {
	Email     string `json:"email,omitempty" jsonschema:"email completo do novo usuário (qualquer domínio); tem precedência sobre localPart"`
	LocalPart string `json:"localPart,omitempty" jsonschema:"parte local que vira <localPart>@santos-tech.com (alternativa a email)"`
	Name      string `json:"name" jsonschema:"nome completo"`
	Role      int    `json:"role" jsonschema:"papel: 1=aluno, 2=professor, 3=admin"`
}

type updateUserInput struct {
	ID         string  `json:"id" jsonschema:"id do usuário"`
	Name       *string `json:"name,omitempty"`
	Role       *int    `json:"role,omitempty" jsonschema:"papel: 1=aluno, 2=professor, 3=admin"`
	Suspended  *bool   `json:"suspended,omitempty" jsonschema:"true suspende, false reativa"`
	QuotaBytes *int64  `json:"quotaBytes,omitempty" jsonschema:"quota da caixa de email em bytes"`
}

type userIDInput struct {
	ID string `json:"id" jsonschema:"id do usuário"`
}

type emptyInput struct{}

func (s *Server) addAuthTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "whoami",
		Description: "Mostra o usuário dono do token atual (GET /auth/me): id, email, papel, permissões.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", s.cfg.AuthBaseURL+"/auth/me", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_users",
		Description: "Lista usuários do ecossistema (admin). Por padrão só contas @santos-tech.com; scope=all inclui todos os domínios.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listUsersInput) (*mcp.CallToolResult, any, error) {
		u := s.cfg.AuthBaseURL + "/auth/admin/users"
		if in.Scope != "" {
			u += "?scope=" + url.QueryEscape(in.Scope)
		}
		return s.proxy(ctx, req, "GET", u, nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_user",
		Description: "Cria usuário sem senha e envia convite por email (admin). Informe email completo OU localPart (vira @santos-tech.com).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createUserInput) (*mcp.CallToolResult, any, error) {
		if in.Email == "" && in.LocalPart == "" {
			return errResult("informe email ou localPart"), nil, nil
		}
		return s.proxy(ctx, req, "POST", s.cfg.AuthBaseURL+"/auth/admin/users", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_user",
		Description: "Atualiza usuário (admin): nome, papel, suspensão e/ou quota da caixa de email. Só os campos enviados mudam.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateUserInput) (*mcp.CallToolResult, any, error) {
		body := map[string]any{}
		if in.Name != nil {
			body["name"] = *in.Name
		}
		if in.Role != nil {
			body["role"] = *in.Role
		}
		if in.Suspended != nil {
			body["suspended"] = *in.Suspended
		}
		if in.QuotaBytes != nil {
			body["quotaBytes"] = *in.QuotaBytes
		}
		if len(body) == 0 {
			return errResult("nenhum campo para atualizar (name, role, suspended, quotaBytes)"), nil, nil
		}
		return s.proxy(ctx, req, "PATCH", s.cfg.AuthBaseURL+"/auth/admin/users/"+url.PathEscape(in.ID), body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "resend_invite",
		Description: "Reenvia o email de convite (definição de senha) de um usuário criado pelo admin.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in userIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "POST", s.cfg.AuthBaseURL+"/auth/admin/users/"+url.PathEscape(in.ID)+"/invite", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ecosystem_status",
		Description: "Saúde agregada do ecossistema santos-tech (auth, banco, redis, email, claude).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", s.cfg.AuthBaseURL+"/status", nil)
	})
}
