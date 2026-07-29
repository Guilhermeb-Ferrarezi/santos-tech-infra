package main

import "net/http"

// handlePortalRankingNotas: leaderboard global por % de acerto (mín. 10 respostas
// corrigidas pra entrar no ranking).
func (s *Server) handlePortalRankingNotas(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalRankingNotas(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

// handlePortalRankingPontos: leaderboard global por soma de pontos.
func (s *Server) handlePortalRankingPontos(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalRankingPontos(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}
