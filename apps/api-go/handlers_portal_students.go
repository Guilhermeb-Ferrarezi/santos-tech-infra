package main

import "net/http"

// GET /portal/students-overview — visão consolidada de alunos pro dashboard
// administrativo (ver dashboard/web Home.tsx): nome, curso, progresso em
// fases ("aulas") e professor (quando class_teacher já tem o vínculo). Rota
// transversal (não é uma "área" específica do portal), mesmo padrão de
// GET /portal/overview e GET /portal/users — ver portal_routes.go.
func (s *Server) handlePortalStudentsOverview(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalStudentsOverview(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

// GET /portal/me/overview — o próprio progresso de quem está logado (Home,
// quando a pessoa não é staff — ver dashboard/web Home.tsx). Rota aberta a
// qualquer sessão autenticada (não passa por portalAnyRead/portalRead): o
// escopo já é a própria pessoa, não precisa de permissão de portal nenhuma.
// Sem matrícula no Portal (a maioria dos usuários do auth central hoje):
// devolve lista vazia, não erro.
func (s *Server) handlePortalMyOverview(w http.ResponseWriter, r *http.Request) {
	u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado"))
		return
	}
	items, err := s.portalMyOverview(r.Context(), u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
