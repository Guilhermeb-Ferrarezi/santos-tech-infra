package main

import "net/http"

// handleListInstitutionalMailboxes lista os emails das caixas institucionais
// (login_disabled=true). Qualquer sessão autenticada pode chamar — não é dado
// sensível (já visível em /auth/admin/users?scope=all pra quem é admin) e o
// serviço `email` precisa disso pra colaboradores comuns, não só admins.
func (s *Server) handleListInstitutionalMailboxes(w http.ResponseWriter, r *http.Request) {
	emails, err := s.listInstitutionalMailboxes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"emails": emails})
}
