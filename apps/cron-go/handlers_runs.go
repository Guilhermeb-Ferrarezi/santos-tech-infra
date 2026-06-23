package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santos-tech/cron-go/db"
)

func (s *Server) jobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id inválido")
		return 0, false
	}
	return id, true
}

func (s *Server) handlePauseJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.jobID(w, r)
	if !ok {
		return
	}
	if _, err := s.q.GetJob(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "job não encontrado")
		return
	}
	if s.q.SetJobEnabled(r.Context(), db.SetJobEnabledParams{ID: id, Enabled: false, NextRunAt: pgtype.Timestamptz{}}) != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao pausar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.jobID(w, r)
	if !ok {
		return
	}
	job, err := s.q.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "job não encontrado")
		return
	}
	next, _ := nextRun(job.ScheduleCron, job.Timezone, time.Now().UTC())
	if s.q.SetJobEnabled(r.Context(), db.SetJobEnabledParams{ID: id, Enabled: true, NextRunAt: pgTimestamp(next)}) != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao retomar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.jobID(w, r)
	if !ok {
		return
	}
	job, err := s.q.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "job não encontrado")
		return
	}
	s.runJobOnce(r.Context(), job) // síncrono: o admin vê o resultado no histórico
	writeJSON(w, http.StatusOK, map[string]string{"status": "executed"})
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := s.jobID(w, r)
	if !ok {
		return
	}
	runs, err := s.q.ListRunsByJob(r.Context(), db.ListRunsByJobParams{JobID: id, Limit: 50})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao listar runs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}
