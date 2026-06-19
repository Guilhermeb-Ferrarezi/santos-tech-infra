package main

import (
	"net/http"
)

// Descoberta OAuth para clientes MCP (claude.ai e afins):
//   - RFC 8414: metadata do authorization server (/.well-known/oauth-authorization-server)
//   - RFC 9728: metadata do protected resource (o MCP em /mcp)
//   - RFC 7591: Dynamic Client Registration (POST /oauth/register)
//
// O fluxo: o cliente MCP toma 401 do gateway com `resource_metadata=...`,
// busca o protected-resource metadata aqui, descobre o authorization server,
// se registra via DCR e roda o authorization code + PKCE já existente.

// writePublicJSON responde JSON liberado para qualquer origem: são endpoints
// públicos de descoberta (sem credencial), e clientes MCP no navegador
// precisam lê-los cross-origin.
func writePublicJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, status, v)
}

// GET /.well-known/oauth-authorization-server
func (s *Server) handleOAuthASMetadata(w http.ResponseWriter, r *http.Request) {
	o := s.cfg.PublicOrigin
	writePublicJSON(w, http.StatusOK, map[string]any{
		"issuer":                                o,
		"authorization_endpoint":                o + "/oauth/authorize",
		"token_endpoint":                        o + "/oauth/token",
		"registration_endpoint":                 o + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{},
	})
}

// GET /.well-known/openid-configuration — OIDC Discovery (OpenID Connect Core §4).
// Permite que clientes OIDC (ex.: Better Auth SSO) descubram os endpoints sem
// configuração manual. Mesmos dados do AS metadata + campos exigidos pelo OIDC.
func (s *Server) handleOIDCDiscovery(w http.ResponseWriter, r *http.Request) {
	o := s.cfg.PublicOrigin
	writePublicJSON(w, http.StatusOK, map[string]any{
		"issuer":                                o,
		"authorization_endpoint":                o + "/oauth/authorize",
		"token_endpoint":                        o + "/oauth/token",
		"userinfo_endpoint":                     o + "/auth/me",
		"jwks_uri":                              o + "/.well-known/jwks.json",
		"registration_endpoint":                 o + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"HS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"openid", "email", "profile"},
		"claims_supported":                      []string{"sub", "email", "name", "id"},
	})
}

// GET /.well-known/jwks.json — JSON Web Key Set (RFC 7517).
// Usamos HS256 (chave simétrica), que não tem representação pública em JWKS.
// O endpoint existe para satisfazer clientes OIDC que exigem jwks_uri no discovery.
func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	writePublicJSON(w, http.StatusOK, map[string]any{
		"keys": []any{},
	})
}

// GET /.well-known/oauth-protected-resource[/mcp] — descreve o MCP como
// resource protegido por este authorization server (RFC 9728; clientes MCP
// inserem o well-known entre host e path do resource, daí a variante /mcp).
func (s *Server) handleProtectedResourceMCP(w http.ResponseWriter, r *http.Request) {
	o := s.cfg.PublicOrigin
	writePublicJSON(w, http.StatusOK, map[string]any{
		"resource":                 o + "/mcp",
		"authorization_servers":    []string{o},
		"bearer_methods_supported": []string{"header"},
	})
}

const (
	dcrMaxRedirectURIs = 5
	dcrMaxNameLen      = 100
)

// dcrError responde no formato de erro da RFC 7591 (difere do nosso padrão).
func dcrError(w http.ResponseWriter, code, desc string) {
	writePublicJSON(w, http.StatusBadRequest, map[string]string{
		"error": code, "error_description": desc,
	})
}

// POST /oauth/register — Dynamic Client Registration (RFC 7591), público com
// rate limit. Só clients públicos (PKCE, sem secret): registrar um client não
// dá acesso a nada — o usuário ainda precisa autorizar no chooser.
func (s *Server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		dcrError(w, "invalid_client_metadata", "corpo JSON inválido")
		return
	}
	if len(body.RedirectURIs) == 0 || len(body.RedirectURIs) > dcrMaxRedirectURIs {
		dcrError(w, "invalid_redirect_uri", "informe de 1 a 5 redirect_uris")
		return
	}
	clientID := "dcr_" + randomToken(16)
	if err := validateOAuthClientInput(clientID, body.RedirectURIs); err != nil {
		dcrError(w, "invalid_redirect_uri", err.Error())
		return
	}
	name := body.ClientName
	if len(name) > dcrMaxNameLen {
		name = name[:dcrMaxNameLen]
	}
	if name == "" {
		name = "App registrado dinamicamente"
	}
	c, err := s.insertOAuthClient(r.Context(), clientID, name, body.RedirectURIs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writePublicJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ClientID,
		"client_id_issued_at":        c.CreatedAt.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}
