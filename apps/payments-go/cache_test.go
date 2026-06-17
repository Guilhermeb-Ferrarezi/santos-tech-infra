package main

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestChargeStatusCache_SetGetInvalidate(t *testing.T) {
	ctx := context.Background()
	s := &Server{rdb: newTestRedis(t)}

	// miss inicial → fail-open
	if _, ok := s.cachedChargeStatus(ctx, "tok"); ok {
		t.Fatal("esperava miss antes de gravar")
	}

	s.cacheChargeStatus(ctx, "tok", "pending")
	if v, ok := s.cachedChargeStatus(ctx, "tok"); !ok || v != "pending" {
		t.Fatalf("esperava hit 'pending', veio (%q, %v)", v, ok)
	}

	// invalidação (mudança de estado pelo webhook) → volta a miss
	s.invalidateChargeStatus(ctx, "tok")
	if _, ok := s.cachedChargeStatus(ctx, "tok"); ok {
		t.Fatal("esperava miss após invalidar")
	}
}

func TestChargeStatusCache_FailOpenSemRedis(t *testing.T) {
	ctx := context.Background()
	s := &Server{} // rdb nil

	// nenhuma dessas chamadas deve entrar em pânico nem bloquear
	s.cacheChargeStatus(ctx, "tok", "pending")
	if _, ok := s.cachedChargeStatus(ctx, "tok"); ok {
		t.Fatal("sem redis deve sempre dar miss (fail-open)")
	}
	s.invalidateChargeStatus(ctx, "tok")
}

func TestNewNotifyPaidTask_Payload(t *testing.T) {
	task := newNotifyPaidTask("tok-123")
	if task.Type() != TaskNotifyPaid {
		t.Fatalf("tipo errado: %s", task.Type())
	}
	if len(task.Payload()) == 0 {
		t.Fatal("payload vazio")
	}
}
