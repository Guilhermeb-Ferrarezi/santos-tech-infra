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
