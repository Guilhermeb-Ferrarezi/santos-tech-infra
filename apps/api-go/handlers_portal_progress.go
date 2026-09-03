package main

import (
	"errors"
	"fmt"
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
	if err := s.portalCanAccessExercise(r.Context(), userIDFrom(r), exerciseID); err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	q := p.Query
	f := portalAnswerFilter{Status: r.URL.Query().Get("status")}
	if v := r.URL.Query().Get("studentId"); v != "" {
		id, e := strconv.ParseInt(v, 10, 64)
		if e != nil || id <= 0 {
			writeErr(w, validationErr("studentId inválido"))
			return
		}
		f.StudentID = id
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
	if err := s.portalCanAccessExercise(r.Context(), userIDFrom(r), exerciseID); err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, total, err := s.portalExerciseAnswerStudents(r.Context(), exerciseID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
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
	if err := s.portalCanAccessStudent(r.Context(), userIDFrom(r), studentID); err != nil {
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
	if err := portalBodyJSON(w, r, &patch); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := patch.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessAnswers(r.Context(), userIDFrom(r), []int64{id}); err != nil {
		writeErr(w, err)
		return
	}
	answer, prev, err := s.portalUpdateAnswer(r.Context(), id, patch)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Resposta"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "answer_correct", "answer", fmt.Sprint(id), map[string]any{
		"previousIsCorrect": prev, "isCorrect": patch.IsCorrect, "feedbackChanged": patch.Feedback != nil,
	})
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer})
}

func (s *Server) handlePortalBatchUpdateAnswers(w http.ResponseWriter, r *http.Request) {
	var in portalAnswerBatchInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessAnswers(r.Context(), userIDFrom(r), in.AnswerIDs); err != nil {
		writeErr(w, err)
		return
	}
	updated, notFound, prev, err := s.portalBatchUpdateAnswers(r.Context(), in.AnswerIDs, in.Patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "answer_correct_batch", "answer", "", map[string]any{
		"updatedCount": len(updated), "notFoundCount": len(notFound),
		"isCorrect": in.Patch.IsCorrect, "feedbackChanged": in.Patch.Feedback != nil,
		"previousIsCorrect": portalPrevSample(prev),
	})
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
	if err := s.portalCanAccessPhase(r.Context(), userIDFrom(r), phaseID); err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, total, err := s.portalPhaseProgress(r.Context(), phaseID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalClassProgress(w http.ResponseWriter, r *http.Request) {
	classID, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), classID); err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, total, err := s.portalClassProgress(r.Context(), classID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

// portalPrevPreviousSampleMax limita quantos valores anteriores de is_correct
// vão para o metadata do log — a coluna "Message" é truncada em 8000 chars e um
// lote pode ter 500 respostas. Acima do teto, a auditoria guarda a amostra e a
// contagem total (updatedCount) continua exata.
const portalPrevPreviousSampleMax = 50

func portalPrevSample(prev map[string]*bool) map[string]*bool {
	if len(prev) <= portalPrevPreviousSampleMax {
		return prev
	}
	out := make(map[string]*bool, portalPrevPreviousSampleMax)
	for k, v := range prev {
		if len(out) == portalPrevPreviousSampleMax {
			break
		}
		out[k] = v
	}
	return out
}
