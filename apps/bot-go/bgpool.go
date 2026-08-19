package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// bgPool executa o processamento em background dos webhooks (Meta, Evolution,
// Coolify) com paralelismo limitado e rastreado.
//
// Antes cada webhook disparava um `go func()` solto: sem teto de concorrência,
// sem WaitGroup e sem context. Como o Shutdown do http.Server só espera os
// HANDLERS retornarem — e o handler retorna logo após o ACK —, todo deploy
// matava o processo no meio de escritas no Postgres e de chamadas HTTP
// externas, perdendo leads em captura e notificações em voo.
type bgPool struct {
	sem            chan struct{}
	wg             sync.WaitGroup
	logger         *slog.Logger
	ctx            context.Context
	cancel         context.CancelFunc
	closed         atomic.Bool
	acquireTimeout time.Duration
}

func newBGPool(slots int, logger *slog.Logger) *bgPool {
	if slots <= 0 {
		slots = 32
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &bgPool{
		sem:            make(chan struct{}, slots),
		logger:         logger,
		ctx:            ctx,
		cancel:         cancel,
		acquireTimeout: 5 * time.Second,
	}
}

// Go agenda fn no pool. Bloqueia até haver slot livre (backpressure — o
// provedor reenvia o webhook) e devolve false se o pool estiver fechado ou se
// não houver slot dentro de acquireTimeout.
//
// O ctx passado a fn é cancelado só quando o Shutdown estoura o timeout, para
// que o trabalho em voo termine em vez de ser abortado no início do shutdown.
func (p *bgPool) Go(name string, fn func(ctx context.Context)) bool {
	if p == nil {
		return false
	}
	if p.closed.Load() {
		p.logger.Warn("bgPool: tarefa recusada, pool em shutdown", "task", name)
		return false
	}

	timer := time.NewTimer(p.acquireTimeout)
	defer timer.Stop()
	select {
	case p.sem <- struct{}{}:
	case <-timer.C:
		bgPoolRejected.WithLabelValues(name).Inc()
		p.logger.Error("bgPool: sem slot livre, tarefa descartada", "task", name)
		return false
	}

	p.wg.Add(1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				p.logger.Error("bgPool: panic na tarefa", "task", name, "panic", r)
			}
			<-p.sem
			p.wg.Done()
		}()
		fn(p.ctx)
	}()
	return true
}

// Shutdown para de aceitar tarefas e espera as em voo até timeout. Estourando o
// prazo, cancela o context delas e devolve false.
func (p *bgPool) Shutdown(timeout time.Duration) bool {
	if p == nil {
		return true
	}
	p.closed.Store(true)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		p.cancel()
		return true
	case <-timer.C:
		p.cancel()
		return false
	}
}
