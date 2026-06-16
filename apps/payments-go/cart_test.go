package main

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestCartAddListClear(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cs := &CartStore{rdb: rdb}
	ctx := context.Background()

	if err := cs.Add(ctx, 7, 100); err != nil {
		t.Fatal(err)
	}
	if err := cs.Add(ctx, 7, 100); err != nil { // mesmo produto soma quantidade
		t.Fatal(err)
	}
	if err := cs.Add(ctx, 7, 200); err != nil {
		t.Fatal(err)
	}
	items, err := cs.List(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("esperava 2 produtos distintos, veio %d", len(items))
	}
	if err := cs.Clear(ctx, 7); err != nil {
		t.Fatal(err)
	}
	items, _ = cs.List(ctx, 7)
	if len(items) != 0 {
		t.Fatalf("carrinho deveria estar vazio, veio %d", len(items))
	}
}
