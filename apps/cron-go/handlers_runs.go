package main

import (
	"context"
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
	// A reserva do run é síncrona (dá o id e detecta sobreposição na hora), mas
	// a EXECUÇÃO vai para uma goroutine com context.Background().
	//
	// Rodar em linha era uma armadilha: o retry interno dorme até 285s mais até
	// 10× o timeout_secs do job, enquanto o http.Server tem WriteTimeout de 30s
	// (main.go). Aos 30s a conexão fechava, o r.Context() era cancelado, o
	// FinishRun ia junto — e a linha ficava presa em status='running'. Como o
	// HasRunningRun considera runs de até 1h, o job agendado era pulado por até
	// 60 minutos por causa de um clique em "rodar agora".
	run, skipped, err := s.claimRunSlot(r.Context(), job.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao criar a execução")
		return
	}
	if skipped {
		writeJSON(w, http.StatusConflict, map[string]any{
			"status": "skipped_overlap",
			"runId":  run.ID,
			"reason": "já existe uma execução em andamento para este job",
		})
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.executeRun(context.Background(), job, run)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "runId": run.ID})
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
