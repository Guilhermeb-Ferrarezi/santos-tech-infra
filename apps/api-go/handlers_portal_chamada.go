package main

import (
	"context"
	"net/http"
	"time"
)

// handlePortalGenerateSessions (POST /portal/classes/{classId}/sessions)
// materializa as aulas a partir da grade semanal, entre duas datas.
// Sem "from", usa a data de início da turma — que é o caso normal: "cadastra
// as aulas desde que esse aluno começou".
func (s *Server) handlePortalGenerateSessions(w http.ResponseWriter, r *http.Request) {
	classID, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if r.ContentLength > 0 {
		if err := portalBodyJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	de, err := s.portalSessionRangeStart(r.Context(), classID, in.From)
	if err != nil {
		writeErr(w, err)
		return
	}
	ate := time.Now()
	if in.To != "" {
		if ate, err = portalParseDate(in.To); err != nil {
			writeErr(w, err)
			return
		}
	}
	criadas, err := s.portalGenerateSessions(r.Context(), classID, de, ate)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"criadas": criadas,
		"de":      de.Format("2006-01-02"),
		"ate":     ate.Format("2006-01-02"),
	})
}

// portalSessionRangeStart resolve a data inicial: a informada, ou o início da
// turma quando nada for informado.
func (s *Server) portalSessionRangeStart(ctx context.Context, classID int64, from string) (time.Time, error) {
	if from != "" {
		return portalParseDate(from)
	}
	var inicio time.Time
	if err := s.portalDB.QueryRow(ctx, `SELECT start_date FROM class WHERE id=$1`, classID).Scan(&inicio); err != nil {
		return time.Time{}, err
	}
	return inicio, nil
}

func (s *Server) handlePortalListSessions(w http.ResponseWriter, r *http.Request) {
	classID, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	itens, err := s.portalListSessions(r.Context(), classID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": itens})
}

// handlePortalSetAttendance (PUT /portal/sessions/{sessionId}/attendance)
func (s *Server) handlePortalSetAttendance(w http.ResponseWriter, r *http.Request) {
	sessionID, err := portalPathID(r, "sessionId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		UserID int64  `json:"userId"`
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.UserID <= 0 {
		writeErr(w, validationErr("userId obrigatório"))
		return
	}
	if err := s.portalSetAttendance(r.Context(), sessionID, in.UserID, in.Status, in.Note); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePortalGerarAulasCron (POST /portal/internal/gerar-aulas) é o alvo do
// cron diário: materializa as aulas recentes de todas as turmas com grade.
// Idempotente — rodar duas vezes no mesmo dia não duplica nada.
func (s *Server) handlePortalGerarAulasCron(w http.ResponseWriter, r *http.Request) {
	turmas, criadas, err := s.portalGerarAulasDeTodasAsTurmas(r.Context(), 7)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"turmasComGrade": turmas, "aulasCriadas": criadas})
}
