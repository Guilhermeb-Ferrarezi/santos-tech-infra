package main

import "net/http"

// handlePortalNotionSync (POST /portal/notion/sync) traz turmas e horários da
// base "Agenda de Aulas" do Notion pro Portal.
//
// dryRun=true (padrão) NÃO grava nada: devolve exatamente o que seria criado,
// linha a linha, com o motivo de cada uma que foi ignorada. Só grava com
// dryRun=false explícito — importar dado bagunçado sem olhar antes é como o
// Portal ganha turma duplicada e horário errado.
func (s *Server) handlePortalNotionSync(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DryRun *bool `json:"dryRun"`
	}
	if r.ContentLength > 0 {
		if err := portalBodyJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	dryRun := in.DryRun == nil || *in.DryRun

	res, err := s.portalNotionSync(r.Context(), dryRun)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
