package main

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// ── Respostas e correção ─────────────────────────────────────────────────────

func (s *Server) handlePortalExerciseAnswers(w http.ResponseWriter, r *http.Request) {
	exerciseID, err := portalPathID(r, "exerciseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	q := p.Query
	f := portalAnswerFilter{Status: r.URL.Query().Get("status")}
	if v := r.URL.Query().Get("studentId"); v != "" {
		if id, e := strconv.ParseInt(v, 10, 64); e == nil {
			f.StudentID = id
		}
	}
	items, stats, total, err := s.portalExerciseAnswers(r.Context(), exerciseID, f, q, r.URL.Query().Get("sort"), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	page := newPortalPage(items, total, p)
	writeJSON(w, http.StatusOK, map[string]any{
		"items": page.Items, "total": page.Total, "pagination": page.Pagination, "stats": stats,
	})
}

func (s *Server) handlePortalExerciseAnswerStudents(w http.ResponseWriter, r *http.Request) {
	exerciseID, err := portalPathID(r, "exerciseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	items, err := s.portalExerciseAnswerStudents(r.Context(), exerciseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handlePortalAnswerStudents(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalAnswerStudents(r.Context(), p.Query, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalStudentAnsweredExercises(w http.ResponseWriter, r *http.Request) {
	studentID, err := portalPathID(r, "studentId")
	if err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, total, err := s.portalStudentAnsweredExercises(r.Context(), studentID, p.Query, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalUpdateAnswer(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "answerId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var patch portalAnswerPatch
	if err := decodePortalJSON(r.Body, &patch); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := patch.validate(); err != nil {
		writeErr(w, err)
		return
	}
	answer, err := s.portalUpdateAnswer(r.Context(), id, patch)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Resposta"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer})
}

func (s *Server) handlePortalBatchUpdateAnswers(w http.ResponseWriter, r *http.Request) {
	var in portalAnswerBatchInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	updated, notFound, err := s.portalBatchUpdateAnswers(r.Context(), in.AnswerIDs, in.Patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updatedCount": len(updated), "updatedIds": updated,
		"notFoundCount": len(notFound), "notFoundIds": notFound,
	})
}

// ── Progresso ────────────────────────────────────────────────────────────────

func (s *Server) handlePortalPhaseProgress(w http.ResponseWriter, r *http.Request) {
	phaseID, err := portalPathID(r, "phaseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	items, err := s.portalPhaseProgress(r.Context(), phaseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handlePortalClassProgress(w http.ResponseWriter, r *http.Request) {
	classID, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	items, err := s.portalClassProgress(r.Context(), classID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
