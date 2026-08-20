package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Shutdown deve esperar o trabalho em voo — é exatamente o que se perdia a cada
// deploy quando o processamento era um `go func()` solto.
func TestBGPoolShutdownEsperaTarefas(t *testing.T) {
	p := newBGPool(4, testLogger())
	var concluidas atomic.Int32

	for i := 0; i < 4; i++ {
		if !p.Go("t", func(ctx context.Context) {
			time.Sleep(50 * time.Millisecond)
			concluidas.Add(1)
		}) {
			t.Fatal("Go deveria aceitar a tarefa")
		}
	}

	if !p.Shutdown(3 * time.Second) {
		t.Fatal("Shutdown estourou o timeout")
	}
	if got := concluidas.Load(); got != 4 {
		t.Fatalf("tarefas concluídas = %d, quer 4", got)
	}
}

func TestBGPoolLimitaParalelismo(t *testing.T) {
	p := newBGPool(2, testLogger())
	var emVoo, pico, aceitas atomic.Int32

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.Go("t", func(ctx context.Context) {
				n := emVoo.Add(1)
				for {
					old := pico.Load()
					if n <= old || pico.CompareAndSwap(old, n) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				emVoo.Add(-1)
			}) {
				aceitas.Add(1)
			}
		}()
	}
	wg.Wait()
	p.Shutdown(5 * time.Second)

	if pico.Load() > 2 {
		t.Fatalf("pico de concorrência = %d, teto era 2", pico.Load())
	}
	if aceitas.Load() != 12 {
		t.Fatalf("tarefas aceitas = %d, quer 12 (nenhuma deveria ser descartada)", aceitas.Load())
	}
}

func TestBGPoolRecusaDepoisDoShutdown(t *testing.T) {
	p := newBGPool(2, testLogger())
	p.Shutdown(time.Second)
	if p.Go("t", func(ctx context.Context) {}) {
		t.Fatal("pool fechado não deveria aceitar tarefa")
	}
}

// Estourando o prazo, o context das tarefas em voo é cancelado para que elas
// possam abortar em vez de segurar o processo.
func TestBGPoolCancelaContextNoTimeout(t *testing.T) {
	p := newBGPool(2, testLogger())
	cancelado := make(chan struct{})

	p.Go("t", func(ctx context.Context) {
		<-ctx.Done()
		close(cancelado)
	})

	if p.Shutdown(100 * time.Millisecond) {
		t.Fatal("Shutdown deveria reportar timeout")
	}
	select {
	case <-cancelado:
	case <-time.After(2 * time.Second):
		t.Fatal("context da tarefa não foi cancelado")
	}
}

func TestBGPoolRecuperaPanic(t *testing.T) {
	p := newBGPool(2, testLogger())
	ok := make(chan struct{})

	p.Go("panica", func(ctx context.Context) { panic("boom") })
	p.Go("normal", func(ctx context.Context) { close(ok) })

	if !p.Shutdown(3 * time.Second) {
		t.Fatal("Shutdown estourou o timeout — slot não foi devolvido após panic")
	}
	select {
	case <-ok:
	default:
		t.Fatal("tarefa seguinte não rodou")
	}
}
