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

// claimRunSlot reserva a vaga de execução do job: pega o lock consultivo do
// job, checa sobreposição e cria o cron_run — tudo na MESMA transação.
//
// Antes, HasRunningRun e o CreateRun seguinte eram dois statements soltos: o
// tick do scheduler e um "rodar agora" simultâneos (ou duas réplicas) passavam
// os dois pela checagem e criavam dois runs para o mesmo job — o skip de
// sobreposição não segurava nada. O pg_advisory_xact_lock(job_id) serializa a
// janela inteira e é liberado sozinho no commit/rollback.
//
// skipped=true significa que já havia run em andamento: a linha 'skipped_overlap'
// é gravada aqui mesmo e não há nada a executar.
func (s *Server) claimRunSlot(ctx context.Context, jobID int64) (run db.CronRun, skipped bool, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return db.CronRun{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op depois do commit
	qtx := s.q.WithTx(tx)

	if err := qtx.LockJobForRun(ctx, jobID); err != nil {
		return db.CronRun{}, false, err
	}
	running, err := qtx.HasRunningRun(ctx, jobID)
	if err != nil {
		return db.CronRun{}, false, err
	}

	run, err = qtx.CreateRun(ctx, db.CreateRunParams{JobID: jobID, Status: "running", Attempt: 1})
	if err != nil {
		return db.CronRun{}, false, err
	}
	if running {
		if err := qtx.FinishRun(ctx, db.FinishRunParams{ID: run.ID, Status: "skipped_overlap", Attempt: 1}); err != nil {
			return db.CronRun{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.CronRun{}, false, err
	}
	return run, running, nil
}

func (s *Server) runJobOnce(ctx context.Context, job db.CronJob) {
	run, skipped, err := s.claimRunSlot(ctx, job.ID)
	if err != nil {
		slog.Error("runJobOnce: falha ao reservar a execução", "job", job.ID, "err", err)
		return
	}
	if skipped {
		cronRunsTotal.WithLabelValues("skipped_overlap").Inc()
		slog.Warn("runJobOnce: overlap detectado, pulando", "job", job.ID)
		return
	}
	s.executeRun(ctx, job, run)
}

// executeRun faz o dispatch (com os retries do job) e fecha o cron_run. Recebe
// o run já criado por claimRunSlot — quem chama decide se roda em linha (tick
// do scheduler) ou numa goroutine própria (disparo manual, ver handleRunJob).
func (s *Server) executeRun(ctx context.Context, job db.CronJob, run db.CronRun) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic em executeRun", "job", job.ID, "run", run.ID, "panic", rec)
		}
	}()

	start := time.Now()
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

	cronRunDuration.Observe(time.Since(start).Seconds())
	cronRunsTotal.WithLabelValues(status).Inc()

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

// ── Retenção do histórico ────────────────────────────────────────────────

const (
	// purgeInterval: de quanto em quanto tempo a rotina de purga acorda.
	purgeInterval = 6 * time.Hour
	// purgeBatch: teto de linhas por passada, para a purga nunca virar um
	// DELETE gigante segurando lock em cima de uma tabela quente. Se sobrar
	// coisa, a próxima passada continua.
	purgeBatch = 5000
)

// RunRetention apaga periodicamente os cron_runs mais velhos que
// cfg.RunRetentionDays. Sem isso nada nunca era apagado: o scheduler tica a
// cada 30s e cada execução grava uma linha com response_excerpt TEXT, então um
// job de 1/min sozinho gera ~525 mil linhas por ano.
//
// RunRetentionDays <= 0 desliga a purga (retenção infinita, explicitamente).
func (s *Server) RunRetention(ctx context.Context) {
	if s.cfg.RunRetentionDays <= 0 {
		slog.Warn("retenção de cron_runs desligada (CRON_RUN_RETENTION_DAYS<=0) — a tabela cresce sem limite")
		return
	}
	// Uma passada no boot para não esperar o primeiro tick de 6h.
	s.purgeOldRuns(ctx)
	t := time.NewTicker(purgeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.purgeOldRuns(ctx)
		}
	}
}

func (s *Server) purgeOldRuns(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic na purga de cron_runs", "panic", rec)
		}
	}()
	for {
		n, err := s.q.PurgeOldRuns(ctx, db.PurgeOldRunsParams{
			RetentionDays: int32(s.cfg.RunRetentionDays),
			MaxRows:       purgeBatch,
		})
		if err != nil {
			slog.Error("purga de cron_runs falhou", "err", err)
			return
		}
		if n > 0 {
			slog.Info("cron_runs purgados", "linhas", n, "retencao_dias", s.cfg.RunRetentionDays)
		}
		if n < purgeBatch {
			return // acabou (ou não havia nada)
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}
