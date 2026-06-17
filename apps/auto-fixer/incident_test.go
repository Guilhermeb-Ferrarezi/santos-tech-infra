package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return &Store{rdb: redis.NewClient(&redis.Options{Addr: mr.Addr()})}
}

func TestIncidentRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := &Incident{ID: "i1", App: "bot-go", Status: "fixing", Attempts: 1}
	if err := s.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, "i1")
	if err != nil || got.App != "bot-go" || got.Attempts != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestActiveByApp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := &Incident{ID: "i1", App: "bot-go", Status: "awaiting_rebuild"}
	_ = s.Save(ctx, in)
	if err := s.SetActive(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ActiveByApp(ctx, "bot-go")
	if got == nil || got.ID != "i1" {
		t.Fatalf("active=%+v", got)
	}
	_ = s.ClearActive(ctx, "bot-go")
	got, _ = s.ActiveByApp(ctx, "bot-go")
	if got != nil {
		t.Fatalf("deveria limpar: %+v", got)
	}
}

func TestBySHAandDedupe(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := &Incident{ID: "i1", App: "bot-go"}
	_ = s.Save(ctx, in)
	_ = s.BindSHA(ctx, "deadbeef", "i1")
	got, _ := s.BySHA(ctx, "deadbeef")
	if got == nil || got.ID != "i1" {
		t.Fatalf("bySHA=%+v", got)
	}
	first, _ := s.DedupeDeployment(ctx, "uuid-1", time.Minute)
	second, _ := s.DedupeDeployment(ctx, "uuid-1", time.Minute)
	if !first || second {
		t.Fatalf("dedupe: first=%v second=%v", first, second)
	}
}
