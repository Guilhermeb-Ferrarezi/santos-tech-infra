package main

// Controle de horas de clientes — handlers HTTP. CRUD de clientes/sessões é
// admin-only (dado financeiro); a rota pública (sem guard) só existe para o
// link que o cliente abre, identificado por token (não por sessão/cookie).

import (
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// isValidHourSessionToken reporta se s é um token bem formado de sessão de
// horas — a saída exata de randomToken(32): 64 caracteres hex minúsculos.
func isValidHourSessionToken(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// ── admin: clientes ──────────────────────────────────────────────────────────

// GET /hour-clients
func (s *Server) handleListHourClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.listHourClients(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": clients})
}

// POST /hour-clients — {name, phone?}
func (s *Server) handleCreateHourClient(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		Name  string  `json:"name"`
		Phone *string `json:"phone"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Nome obrigatório (até 200 caracteres)"))
		return
	}
	c, err := s.insertHourClient(r.Context(), in.Name, in.Phone)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"client": c})
}

// POST /hour-clients/{id}/purchases — {minutesAdded, note?}
func (s *Server) handleAddHourPurchase(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourClientNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		MinutesAdded int     `json:"minutesAdded"`
		Note         *string `json:"note"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if in.MinutesAdded == 0 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "minutesAdded não pode ser zero"))
		return
	}
	c, err := s.addHourPurchase(r.Context(), id, in.MinutesAdded, in.Note, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"client": c})
}

// PATCH /hour-clients/{id} — {discountPercent}: desconto padrão (0-100)
// aplicado no faturamento avulso deste cliente (ver GET /hour-billing).
func (s *Server) handleUpdateHourClientDiscount(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourClientNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		DiscountPercent int `json:"discountPercent"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if in.DiscountPercent < 0 || in.DiscountPercent > 100 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "discountPercent precisa estar entre 0 e 100"))
		return
	}
	c, err := s.updateHourClientDiscount(r.Context(), id, in.DiscountPercent)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"client": c})
}

// ── admin: sessões ───────────────────────────────────────────────────────────

// GET /hour-sessions — painel "ao vivo" (sessões ainda não encerradas)
func (s *Server) handleListHourSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.listActiveHourSessions(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// POST /hour-sessions — {clientId} -> {session, token, publicUrl, shortCode}
func (s *Server) handleStartHourSession(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		ClientID string `json:"clientId"`
		// ScheduledEndAt: opcional, timestamp absoluto (RFC3339) já resolvido
		// pelo front a partir de duração OU horário fixo — ver
		// autoEndIfDue em hour_sessions.go pra como isso encerra sozinho.
		ScheduledEndAt *time.Time `json:"scheduledEndAt"`
		// ScheduledStartAt: opcional, timestamp absoluto (RFC3339). Futuro =
		// sessão nasce "scheduled" e inicia sozinha na hora (ver
		// autoStartIfDue); ausente = começa agora, elapsed=0; PASSADO = início
		// retroativo — sessão já nasce "active" mas o evento 'start' é
		// backdatado pra esse horário, então o tempo decorrido já conta desde
		// lá (admin esqueceu de abrir a sessão e o cliente já tinha começado).
		ScheduledStartAt *time.Time `json:"scheduledStartAt"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(in.ClientID) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "clientId inválido"))
		return
	}
	if in.ScheduledEndAt != nil && !in.ScheduledEndAt.After(time.Now()) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "scheduledEndAt precisa ser no futuro"))
		return
	}
	if in.ScheduledStartAt != nil && in.ScheduledEndAt != nil && !in.ScheduledEndAt.After(*in.ScheduledStartAt) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "scheduledEndAt precisa ser depois de scheduledStartAt"))
		return
	}
	client, err := s.getHourClient(r.Context(), in.ClientID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if client == nil {
		writeErr(w, errHourClientNotFound)
		return
	}
	h, token, shortCode, err := s.startHourSession(r.Context(), in.ClientID, userIDFrom(r), in.ScheduledEndAt, in.ScheduledStartAt)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"session":   h,
		"token":     token,
		"publicUrl": s.cfg.DashboardWebOrigin + "/sessao/" + token,
		"shortCode": shortCode,
	})
}

// POST /public/hour-sessions/pair-by-code — {code} -> {token, publicUrl}
// Trocado por um token novo (reemitido) e o código fica inutilizado — ver
// pairHourSessionByCode. Sem authGuard: o código curto É a credencial (posse
// = acesso), mesmo espírito do token na URL da rota pública de leitura.
func (s *Server) handlePairHourSessionByCode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Code = strings.TrimSpace(in.Code)
	if !isValidHourSessionShortCode(in.Code) {
		writeErr(w, errHourSessionCodeInvalid)
		return
	}
	token, err := s.pairHourSessionByCode(r.Context(), in.Code)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"publicUrl": s.cfg.DashboardWebOrigin + "/sessao/" + token,
	})
}

// isValidHourSessionShortCode reporta se s é a saída exata de randomDigits(6).
func isValidHourSessionShortCode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// POST /hour-sessions/{id}/link — gera (ou reemite) o link público da sessão.
// Não depende de cache local: cobre sessão iniciada antes dessa função
// existir, noutro navegador, ou com o link perdido. O token antigo, se
// houver, para de funcionar.
func (s *Server) handleReissueHourSessionLink(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	token, err := s.reissueHourSessionToken(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"publicUrl": s.cfg.DashboardWebOrigin + "/sessao/" + token,
	})
}

// POST /hour-sessions/{id}/pause
func (s *Server) handlePauseHourSession(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	h, err := s.transitionHourSession(r.Context(), id, userIDFrom(r), "active", "paused", "pause")
	if err != nil {
		writeErr(w, err)
		return
	}
	// Pausa manual do admin também limpa um eventual pedido de pausa pendente.
	_ = s.denyHourSessionPauseRequest(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"session": h})
}

// POST /hour-sessions/{id}/resume
func (s *Server) handleResumeHourSession(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	h, err := s.transitionHourSession(r.Context(), id, userIDFrom(r), "paused", "active", "resume")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": h})
}

// POST /hour-sessions/{id}/end — debita o saldo pelo tempo decorrido
func (s *Server) handleEndHourSession(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	h, err := s.endHourSession(r.Context(), id, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": h})
}

// POST /hour-sessions/{id}/deny-pause — recusa o pedido do cliente sem pausar
func (s *Server) handleDenyHourSessionPause(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.denyHourSessionPauseRequest(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── público (sem auth) ───────────────────────────────────────────────────────

// GET /public/hour-sessions/{token}
func (s *Server) handleGetPublicHourSession(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !isValidHourSessionToken(token) {
		writeErr(w, errHourSessionNotFound)
		return
	}
	h, err := s.getHourSessionByTokenHash(r.Context(), sha256Hex(token))
	if err != nil {
		writeErr(w, err)
		return
	}
	if h == nil {
		writeErr(w, errHourSessionNotFound)
		return
	}
	remainingMinutes := h.BalanceMinutes - int(h.ElapsedSeconds/60)
	writeJSON(w, http.StatusOK, map[string]any{
		"clientName": h.ClientName,
		"status":     h.Status,
		// scheduledStartAt: só relevante pro app do PC quando status é
		// "scheduled" (mostra "começa às HH:MM" em vez do cronômetro).
		"scheduledStartAt": h.ScheduledStartAt,
		"elapsedSeconds":   h.ElapsedSeconds,
		"remainingMinutes": remainingMinutes,
		"pauseRequested":   h.PauseRequestedAt != nil,
	})
}

// POST /public/hour-sessions/{token}/request-pause
func (s *Server) handleRequestHourSessionPause(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !isValidHourSessionToken(token) {
		writeErr(w, errHourSessionNotFound)
		return
	}
	if err := s.requestHourSessionPause(r.Context(), sha256Hex(token)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hourUUIDFrom valida o path param como UUID — mesmo espírito de boardIDFrom
// (handlers_boards.go): formato malformado é indistinguível de recurso
// inexistente, então devolve o notFound do domínio chamador.
func hourUUIDFrom(r *http.Request, param string, notFound error) (string, error) {
	id := r.PathValue(param)
	if !uuidRe.MatchString(id) {
		return "", notFound
	}
	return id, nil
}

// GET /hour-sessions/{id}/events — histórico da sessão.
//
// start/pause/resume/end sempre foram gravados com autor e horário; esta rota
// é o que finalmente os mostra. Inclui os ajustes manuais de tempo.
func (s *Server) handleListHourSessionEvents(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	events, err := s.listHourSessionEvents(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// POST /hour-sessions/{id}/adjust — {deltaSeconds, note?}
//
// Corrige a duração da sessão (esqueceu de pausar, PC caiu, cliente saiu
// antes). deltaSeconds positivo soma, negativo desconta, entre -24h e +24h.
func (s *Server) handleAdjustHourSession(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		DeltaSeconds int64  `json:"deltaSeconds"`
		Note         string `json:"note"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	var note *string
	if n := strings.TrimSpace(in.Note); n != "" {
		if len(n) > 200 {
			writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Nota longa demais (máximo 200 caracteres)"))
			return
		}
		note = &n
	}
	h, err := s.adjustHourSession(r.Context(), id, userIDFrom(r), in.DeltaSeconds, note)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": h})
}

// ── admin: faturamento avulso ────────────────────────────────────────────────

// GET /hour-billing?from=RFC3339&to=RFC3339 — quanto cada cliente usou avulso
// (sem saldo pré-pago suficiente) no período, já convertido em R$ (bruto e
// líquido, com o desconto padrão do cliente). Sem from/to: últimos 30 dias.
func (s *Server) handleGetHourBilling(w http.ResponseWriter, r *http.Request) {
	to := time.Now()
	from := to.Add(-30 * 24 * time.Hour)
	if v := r.URL.Query().Get("to"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "to inválido (use RFC3339)"))
			return
		}
		to = parsed
	}
	if v := r.URL.Query().Get("from"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "from inválido (use RFC3339)"))
			return
		}
		from = parsed
	}
	if !to.After(from) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "to precisa ser depois de from"))
		return
	}
	rows, err := s.listHourBilling(r.Context(), from, to)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "from": from, "to": to})
}
