package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/santos-tech/cron-go/db"
)

// backoff devolve o tempo de espera para a tentativa `attempt` (quadrático, teto 5min).
func backoff(attempt int) time.Duration {
	d := time.Duration(attempt*attempt) * time.Second
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

// RunScheduler acorda a cada 30s e processa os jobs vencidos. Para no ctx.Done().
func (s *Server) RunScheduler(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Server) tick(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic no tick do scheduler", "panic", rec)
		}
	}()

	// Transação para o claim com SKIP LOCKED — segura com múltiplas réplicas.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		slog.Error("tick: begin falhou", "err", err)
		return
	}
	qtx := db.New(tx)
	jobs, err := qtx.ClaimDueJobs(ctx, 20)
	if err != nil {
		_ = tx.Rollback(ctx)
		slog.Error("tick: claim falhou", "err", err)
		return
	}

	// Recalcula next_run_at já dentro da tx (evita re-claim no próximo tick).
	for _, job := range jobs {
		next, errN := nextRun(job.ScheduleCron, job.Timezone, time.Now().UTC())
		if errN != nil {
			slog.Error("cron inválido em job; pulando recompute", "job", job.ID, "err", errN)
			continue
		}
		if err := qtx.UpdateJobAfterRun(ctx, db.UpdateJobAfterRunParams{
			ID:        job.ID,
			NextRunAt: pgTimestamp(next),
		}); err != nil {
			slog.Error("tick: UpdateJobAfterRun falhou", "job", job.ID, "err", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("tick: commit falhou", "err", err)
		return
	}

	// Dispara fora da tx — chamadas HTTP não devem segurar locks. O WaitGroup
	// permite drenar os dispatches em voo no shutdown (ver Server.WaitDrain).
	// Usa context.Background() de propósito: um run em andamento deve COMPLETAR
	// (gravar o cron_run) mesmo durante o shutdown, não ser cancelado no meio.
	for _, job := range jobs {
		s.wg.Add(1)
		go func(j db.CronJob) {
			defer s.wg.Done()
			s.runJobOnce(context.Background(), j)
		}(job)
	}
}

func (s *Server) runJobOnce(ctx context.Context, job db.CronJob) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic em runJobOnce", "job", job.ID, "panic", rec)
		}
	}()

	// Skip de sobreposição: já existe run 'running' para este job.
	running, err := s.q.HasRunningRun(ctx, job.ID)
	if err != nil {
		slog.Error("runJobOnce: erro ao checar overlap", "job", job.ID, "err", err)
		return
	}
	if running {
		run, err := s.q.CreateRun(ctx, db.CreateRunParams{
			JobID:   job.ID,
			Status:  "running",
			Attempt: 1,
		})
		if err == nil {
			_ = s.q.FinishRun(ctx, db.FinishRunParams{
				ID:      run.ID,
				Status:  "skipped_overlap",
				Attempt: 1,
			})
		}
		slog.Warn("runJobOnce: overlap detectado, pulando", "job", job.ID)
		return
	}

	run, err := s.q.CreateRun(ctx, db.CreateRunParams{
		JobID:   job.ID,
		Status:  "running",
		Attempt: 1,
	})
	if err != nil {
		slog.Error("falha ao criar run", "job", job.ID, "err", err)
		return
	}

	var last dispatchResult
	attempt := 1
	maxRetries := int(job.MaxRetries)
	if maxRetries < 1 {
		maxRetries = 1
	}
	for ; attempt <= maxRetries; attempt++ {
		last = s.dispatch(ctx, job)
		if last.Err == nil {
			break
		}
		if attempt < maxRetries {
			time.Sleep(backoff(attempt))
		}
	}

	status := "success"
	errStr := ""
	if last.Err != nil {
		status = "failed"
		errStr = last.Err.Error()
	}

	var httpStatus *int32
	if last.HTTPStatus != 0 {
		v := int32(last.HTTPStatus)
		httpStatus = &v
	}

	if err := s.q.FinishRun(ctx, db.FinishRunParams{
		ID:              run.ID,
		Status:          status,
		HttpStatus:      httpStatus,
		ResponseExcerpt: redact(last.Excerpt),
		Error:           errStr,
		Attempt:         int32(min(attempt, maxRetries)),
	}); err != nil {
		slog.Error("falha ao finalizar run", "job", job.ID, "run", run.ID, "err", err)
	}
}
