package main

import (
	"net/http"
	"slices"
)

// GET /auth/accounts — contas conhecidas neste navegador (cookie assinado),
// já podadas das sessões mortas. "active" = a sessão do refresh cookie atual.
func (s *Server) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ids := s.readAccounts(r)
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"accounts": []AccountSummary{}})
		return
	}
	activeSID := ""
	if c, err := r.Cookie("refresh_token"); err == nil && c.Value != "" {
		if sid, _, _, e := s.sessionByHash(r.Context(), hashRefreshToken(c.Value)); e == nil {
			activeSID = sid
		}
	}
	accounts, err := s.accountSummaries(r.Context(), ids)
	if err != nil {
		writeErr(w, err)
		return
	}
	alive := make([]string, 0, len(accounts))
	for i := range accounts {
		accounts[i].Active = accounts[i].SessionID == activeSID
		alive = append(alive, accounts[i].SessionID)
	}
	if len(alive) != len(ids) {
		s.writeAccounts(w, alive) // auto-limpeza: mortas saem do cookie
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

// DELETE /auth/accounts/{sessionId} — tira a conta da lista e revoga a sessão.
func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sessionId")
	if !slices.Contains(s.readAccounts(r), sid) {
		writeErr(w, appErr(http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Conta não encontrada neste navegador"))
		return
	}
	_ = s.deleteSession(r.Context(), sid)
	s.removeAccount(w, r, sid)
	w.WriteHeader(http.StatusNoContent)
}

// POST /auth/accounts/{sessionId}/activate — troca a conta ativa no auth-web:
// rotaciona a sessão escolhida (a antiga é apagada) e seta os cookies ativos.
func (s *Server) handleAccountActivate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sid := r.PathValue("sessionId")
	if !slices.Contains(s.readAccounts(r), sid) {
		writeErr(w, appErr(http.StatusUnauthorized, "SESSION_EXPIRED", "Sessão não encontrada neste navegador"))
		return
	}
	u, err := s.sessionUserByID(r.Context(), sid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		s.removeAccount(w, r, sid) // auto-limpeza
		writeErr(w, appErr(http.StatusUnauthorized, "SESSION_EXPIRED", "Sessão expirada — entre novamente"))
		return
	}
	if u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}
	// Rotaciona: emite a nova sessão primeiro (se falhar, a antiga segue válida)
	// e só então revoga a antiga no banco. replaceSIDs já tira o sid do cookie.
	if err := s.issueSession(r.Context(), w, r, u, sid); err != nil {
		writeErr(w, err)
		return
	}
	_ = s.deleteSession(r.Context(), sid)
	writeJSON(w, http.StatusOK, map[string]any{"user": s.buildProfile(r.Context(), u)})
}
