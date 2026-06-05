package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	uriLLMS    = "santos-tech://llms.txt"
	uriOpenAPI = "santos-tech://openapi/auth"
)

func (s *Server) addResources(srv *mcp.Server) {
	srv.AddResource(&mcp.Resource{
		URI:         uriLLMS,
		Name:        "llms.txt",
		Description: "Documentação viva de todas as APIs do ecossistema santos-tech (auth, claude agent, email).",
		MIMEType:    "text/markdown",
	}, s.readLLMS)

	if len(s.openapi) > 0 {
		srv.AddResource(&mcp.Resource{
			URI:         uriOpenAPI,
			Name:        "openapi-auth",
			Description: "Contrato OpenAPI 3.1 da Auth API central (api.santos-tech.com/auth).",
			MIMEType:    "application/yaml",
		}, s.readOpenAPI)
	}
}

// readLLMS proxia GET /llms.txt do auth central com o token do usuário —
// sempre a versão em produção, nunca uma cópia desatualizada.
func (s *Server) readLLMS(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	status, raw, err := s.client.do(ctx, "GET", s.cfg.AuthBaseURL+"/llms.txt", authorization(req.Extra), nil)
	if err != nil {
		return nil, fmt.Errorf("llms.txt indisponível: %w", err)
	}
	if status >= 400 {
		return nil, fmt.Errorf("llms.txt: HTTP %d: %s", status, strings.TrimSpace(string(raw)))
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{URI: uriLLMS, MIMEType: "text/markdown", Text: string(raw)},
		},
	}, nil
}

func (s *Server) readOpenAPI(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{URI: uriOpenAPI, MIMEType: "application/yaml", Text: string(s.openapi)},
		},
	}, nil
}
