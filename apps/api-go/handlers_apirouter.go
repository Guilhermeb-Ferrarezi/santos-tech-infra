package main

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/santos-tech/auth/db"
)

// apiRouterNotConfigured é o 503 comum a todo handler quando API_VAULT_SECRET
// não está setada — sem a chave de cifra não há como guardar/usar segredos.
func (s *Server) apiRouterNotConfigured(w http.ResponseWriter) bool {
	if s.vault == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED", "Roteador de APIs indisponível: API_VAULT_SECRET não configurada"))
		return true
	}
	return false
}

func apiRouterPathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("id inválido")
	}
	return id, nil
}

// ── JSON ─────────────────────────────────────────────────────────────────────

func apiRouterProviderJSON(p *db.ApiRouterProvider, counts map[string]int64) map[string]any {
	if counts == nil {
		counts = map[string]int64{}
	}
	return map[string]any{
		"id": p.ID, "name": p.Name, "baseUrl": p.BaseUrl,
		"authHeader": p.AuthHeader, "authScheme": p.AuthScheme,
		"unauthorizedCodes": p.UnauthorizedCodes, "noCreditCodes": p.NoCreditCodes,
		"testPath": p.TestPath, "testMethod": p.TestMethod, "keyCounts": counts,
		"createdAt": p.CreatedAt.Time, "updatedAt": p.UpdatedAt.Time,
	}
}

func apiRouterKeyJSON(k *db.ApiRouterKey) map[string]any {
	m := map[string]any{
		"id": k.ID, "providerId": k.ProviderID, "label": k.Label, "secretTail": k.SecretTail,
		"status": k.Status, "priority": k.Priority, "failureCount": k.FailureCount,
		"createdAt": k.CreatedAt.Time, "updatedAt": k.UpdatedAt.Time,
		"lastUsedAt": nil, "lastErrorAt": nil, "lastErrorCode": nil,
	}
	if k.LastUsedAt.Valid {
		m["lastUsedAt"] = k.LastUsedAt.Time
	}
	if k.LastErrorAt.Valid {
		m["lastErrorAt"] = k.LastErrorAt.Time
	}
	if k.LastErrorCode.Valid {
		m["lastErrorCode"] = k.LastErrorCode.Int32
	}
	return m
}

// ── providers ────────────────────────────────────────────────────────────────

// GET /auth/admin/api-router/providers
func (s *Server) handleListAPIRouterProviders(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	providers, err := s.q.ListAPIRouterProviders(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	counts, err := s.q.CountAPIRouterKeysByProvider(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	byProvider := make(map[int64]map[string]int64, len(providers))
	for _, c := range counts {
		if byProvider[c.ProviderID] == nil {
			byProvider[c.ProviderID] = make(map[string]int64)
		}
		byProvider[c.ProviderID][c.Status] = c.Total
	}
	out := make([]map[string]any, 0, len(providers))
	for _, p := range providers {
		out = append(out, apiRouterProviderJSON(&p, byProvider[p.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

type apiRouterProviderBody struct {
	Name              string  `json:"name"`
	BaseURL           string  `json:"baseUrl"`
	AuthHeader        string  `json:"authHeader"`
	AuthScheme        string  `json:"authScheme"`
	UnauthorizedCodes []int32 `json:"unauthorizedCodes"`
	NoCreditCodes     []int32 `json:"noCreditCodes"`
	TestPath          string  `json:"testPath"`
	TestMethod        string  `json:"testMethod"`
}

// defaults aplica os valores padrão do provider quando o campo vier vazio do
// cliente (auth_header/unauthorized_codes/no_credit_codes/test_method).
func (b apiRouterProviderBody) defaults() apiRouterProviderBody {
	if b.AuthHeader == "" {
		b.AuthHeader = "Authorization"
	}
	if len(b.UnauthorizedCodes) == 0 {
		b.UnauthorizedCodes = []int32{401}
	}
	if len(b.NoCreditCodes) == 0 {
		b.NoCreditCodes = []int32{402, 429}
	}
	if b.TestMethod == "" {
		b.TestMethod = http.MethodGet
	}
	return b
}

// POST /auth/admin/api-router/providers
func (s *Server) handleCreateAPIRouterProvider(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body apiRouterProviderBody
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "corpo inválido"))
		return
	}
	if body.Name == "" || body.BaseURL == "" {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "name e baseUrl são obrigatórios"))
		return
	}
	body = body.defaults()

	p, err := s.q.InsertAPIRouterProvider(r.Context(), db.InsertAPIRouterProviderParams{
		Name: body.Name, BaseUrl: body.BaseURL, AuthHeader: body.AuthHeader, AuthScheme: body.AuthScheme,
		UnauthorizedCodes: body.UnauthorizedCodes, NoCreditCodes: body.NoCreditCodes,
		TestPath: body.TestPath, TestMethod: body.TestMethod,
	})
	if err != nil {
		writeErr(w, appErr(http.StatusConflict, "CONFLICT", "já existe um provider com esse nome"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"provider": apiRouterProviderJSON(&p, nil)})
}

// PATCH /auth/admin/api-router/providers/{id}
func (s *Server) handleUpdateAPIRouterProvider(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	id, err := apiRouterPathID(r, "id")
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body apiRouterProviderBody
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "corpo inválido"))
		return
	}
	if body.Name == "" || body.BaseURL == "" {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "name e baseUrl são obrigatórios"))
		return
	}
	body = body.defaults()

	p, err := s.q.UpdateAPIRouterProvider(r.Context(), db.UpdateAPIRouterProviderParams{
		ID: id, Name: body.Name, BaseUrl: body.BaseURL, AuthHeader: body.AuthHeader, AuthScheme: body.AuthScheme,
		UnauthorizedCodes: body.UnauthorizedCodes, NoCreditCodes: body.NoCreditCodes,
		TestPath: body.TestPath, TestMethod: body.TestMethod,
	})
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "provider não encontrado"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": apiRouterProviderJSON(&p, nil)})
}

// DELETE /auth/admin/api-router/providers/{id}
func (s *Server) handleDeleteAPIRouterProvider(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	id, err := apiRouterPathID(r, "id")
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	n, err := s.q.DeleteAPIRouterProvider(r.Context(), id)
	if err != nil || n == 0 {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "provider não encontrado"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── keys ─────────────────────────────────────────────────────────────────────

// GET /auth/admin/api-router/providers/{id}/keys
func (s *Server) handleListAPIRouterKeys(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	providerID, err := apiRouterPathID(r, "id")
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	// A query por provider_id inexistente também devolve zero linhas (não é
	// erro de SQL) — checagem explícita evita 200 com lista vazia pra um id
	// que não existe.
	if _, err := s.q.GetAPIRouterProvider(r.Context(), providerID); err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "provider não encontrado"))
		return
	}
	keys, err := s.q.ListAPIRouterKeys(r.Context(), providerID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, apiRouterKeyJSON(&k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

const apiRouterMaxSecretLen = 8 << 10 // 8KB — generoso o bastante pra qualquer token/JWT real

// POST /auth/admin/api-router/providers/{id}/keys
func (s *Server) handleCreateAPIRouterKey(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	providerID, err := apiRouterPathID(r, "id")
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body struct {
		Label    string `json:"label"`
		Secret   string `json:"secret"`
		Priority int32  `json:"priority"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "corpo inválido"))
		return
	}
	if body.Label == "" || body.Secret == "" {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "label e secret são obrigatórios"))
		return
	}
	if len(body.Secret) > apiRouterMaxSecretLen {
		writeErr(w, appErr(http.StatusBadRequest, "SECRET_TOO_LARGE", "valor da chave grande demais"))
		return
	}
	if _, err := s.q.GetAPIRouterProvider(r.Context(), providerID); err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "provider não encontrado"))
		return
	}

	enc, err := s.vault.Encrypt(body.Secret)
	if err != nil {
		writeErr(w, err)
		return
	}
	k, err := s.q.InsertAPIRouterKey(r.Context(), db.InsertAPIRouterKeyParams{
		ProviderID: providerID, Label: body.Label, SecretEnc: enc, SecretTail: maskSecret(body.Secret), Priority: body.Priority,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": apiRouterKeyJSON(&k)})
}

// PATCH /auth/admin/api-router/providers/{id}/keys/{keyId}
func (s *Server) handleUpdateAPIRouterKey(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	providerID, err1 := apiRouterPathID(r, "id")
	keyID, err2 := apiRouterPathID(r, "keyId")
	if err1 != nil || err2 != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body struct {
		Label    string `json:"label"`
		Priority int32  `json:"priority"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "corpo inválido"))
		return
	}
	if body.Label == "" {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "label é obrigatório"))
		return
	}
	k, err := s.q.UpdateAPIRouterKeyMeta(r.Context(), db.UpdateAPIRouterKeyMetaParams{
		ID: keyID, ProviderID: providerID, Label: body.Label, Priority: body.Priority,
	})
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "chave não encontrada"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": apiRouterKeyJSON(&k)})
}

// DELETE /auth/admin/api-router/providers/{id}/keys/{keyId}
func (s *Server) handleDeleteAPIRouterKey(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	providerID, err1 := apiRouterPathID(r, "id")
	keyID, err2 := apiRouterPathID(r, "keyId")
	if err1 != nil || err2 != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	n, err := s.q.DeleteAPIRouterKey(r.Context(), db.DeleteAPIRouterKeyParams{ID: keyID, ProviderID: providerID})
	if err != nil || n == 0 {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "chave não encontrada"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /auth/admin/api-router/providers/{id}/keys/{keyId}/status — troca
// MANUAL: só "active" (reativa) ou "disabled" (desliga). "unauthorized"/
// "no_credits" só o roteador atribui sozinho.
func (s *Server) handleSetAPIRouterKeyStatus(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	providerID, err1 := apiRouterPathID(r, "id")
	keyID, err2 := apiRouterPathID(r, "keyId")
	if err1 != nil || err2 != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var body struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "corpo inválido"))
		return
	}
	if body.Status != "active" && body.Status != "disabled" {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_STATUS", "status deve ser active ou disabled"))
		return
	}
	k, err := s.q.SetAPIRouterKeyStatus(r.Context(), db.SetAPIRouterKeyStatusParams{ID: keyID, ProviderID: providerID, Status: body.Status})
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "chave não encontrada"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": apiRouterKeyJSON(&k)})
}

// POST /auth/admin/api-router/providers/{id}/keys/{keyId}/test — dispara uma
// requisição real contra o provider com essa chave específica (mesmo que ela
// não esteja "active") e grava o resultado.
func (s *Server) handleTestAPIRouterKey(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	providerID, err1 := apiRouterPathID(r, "id")
	keyID, err2 := apiRouterPathID(r, "keyId")
	if err1 != nil || err2 != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	provider, err := s.q.GetAPIRouterProvider(r.Context(), providerID)
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "provider não encontrado"))
		return
	}
	key, err := s.q.GetAPIRouterKey(r.Context(), db.GetAPIRouterKeyParams{ID: keyID, ProviderID: providerID})
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "chave não encontrada"))
		return
	}
	res, err := s.testAPIRouterKey(r.Context(), provider, key)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "TEST_FAILED", "falha ao testar chave"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"statusCode": res.StatusCode, "outcome": res.Outcome, "error": res.Error})
}
