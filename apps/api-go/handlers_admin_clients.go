package main

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Gestão admin das aplicações OAuth ("Entrar com Santos Tech").
// Padrão: handlers_admin_users.go (adminGuard nas rotas).

var clientIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Esquema de deep link de app (ex.: santostech, com.exemplo.app). Segue o
// formato de scheme da RFC 3986: começa com letra, seguido de letras/dígitos/
// + - . — não inclui ':' nem '/'.
var appSchemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

// Esquemes que NUNCA podem ser redirect_uri: o valor flui pra window.location
// no front, então estes seriam XSS/exfiltração. Bloqueio explícito (fail-closed).
var dangerousSchemes = map[string]bool{
	"javascript": true, "data": true, "vbscript": true,
	"file": true, "blob": true, "about": true,
}

func validateOAuthClientInput(clientID string, uris []string) error {
	if !clientIDRe.MatchString(clientID) {
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "client_id deve ter 1-64 chars alfanuméricos, _ ou -")
	}
	if len(uris) == 0 {
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "informe ao menos um redirect_uri")
	}
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" {
			return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "redirect_uri inválido: "+raw)
		}
		scheme := strings.ToLower(u.Scheme)
		switch {
		case dangerousSchemes[scheme]:
			return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "redirect_uri com esquema não permitido: "+raw)
		case scheme == "http" || scheme == "https":
			// Web: exige host (bloqueia http:/// e similares).
			if u.Host == "" {
				return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "redirect_uri inválido: "+raw)
			}
		default:
			// Deep link de app (ex.: santostech://auth). Aceita esquema custom
			// válido por RFC 3986, exigindo um caminho/host após o "://".
			if !appSchemeRe.MatchString(scheme) || u.Opaque != "" || (u.Host == "" && u.Path == "") {
				return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "redirect_uri inválido: "+raw)
			}
		}
	}
	return nil
}

// GET /auth/admin/oauth-clients
func (s *Server) handleListOAuthClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.listOAuthClients(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": clients})
}

// POST /auth/admin/oauth-clients {clientId, name, redirectUris}
func (s *Server) handleCreateOAuthClient(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		ClientID     string   `json:"clientId"`
		Name         string   `json:"name"`
		RedirectURIs []string `json:"redirectUris"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "clientId, name e redirectUris são obrigatórios"))
		return
	}
	if err := validateOAuthClientInput(body.ClientID, body.RedirectURIs); err != nil {
		writeErr(w, err)
		return
	}
	if existing, err := s.oauthClientByClientID(r.Context(), body.ClientID); err != nil {
		writeErr(w, err)
		return
	} else if existing != nil {
		writeErr(w, appErr(http.StatusConflict, "CLIENT_ALREADY_EXISTS", "Já existe uma aplicação com este client_id"))
		return
	}
	c, err := s.insertOAuthClient(r.Context(), body.ClientID, body.Name, body.RedirectURIs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// PATCH /auth/admin/oauth-clients/{id} {name?, redirectUris?, isActive?}
func (s *Server) handleUpdateOAuthClient(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !isValidUUID(r.PathValue("id")) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	var body struct {
		Name         *string  `json:"name"`
		RedirectURIs []string `json:"redirectUris"`
		IsActive     *bool    `json:"isActive"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	// redirectUris ausente (nil) = não mexe; presente precisa ser válido.
	if body.RedirectURIs != nil {
		if err := validateOAuthClientInput("placeholder", body.RedirectURIs); err != nil {
			writeErr(w, err)
			return
		}
	}
	c, err := s.updateOAuthClient(r.Context(), r.PathValue("id"), body.Name, body.RedirectURIs, body.IsActive)
	if err != nil {
		writeErr(w, err)
		return
	}
	if c == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Aplicação não encontrada"))
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// DELETE /auth/admin/oauth-clients/{id}
func (s *Server) handleDeleteOAuthClient(w http.ResponseWriter, r *http.Request) {
	if !isValidUUID(r.PathValue("id")) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	ok, err := s.deleteOAuthClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Aplicação não encontrada"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
