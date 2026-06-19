package main

import (
	"testing"
	"time"
)

func TestParseRange(t *testing.T) {
	if r := parseRange(""); r.Key != "30d" || r.Bucket != "day" {
		t.Fatalf("default deveria ser 30d/day, veio %s/%s", r.Key, r.Bucket)
	}
	if r := parseRange("90d"); r.Bucket != "day" {
		t.Fatalf("90d deveria bucketar por day, veio %s", r.Bucket)
	}
	if r := parseRange("12m"); r.Bucket != "month" {
		t.Fatalf("12m deveria bucketar por month, veio %s", r.Bucket)
	}
	if r := parseRange("all"); r.Bucket != "month" {
		t.Fatalf("all deveria bucketar por month, veio %s", r.Bucket)
	}
	if r := parseRange("lixo"); r.Key != "30d" {
		t.Fatalf("valor inválido deveria cair no default 30d, veio %s", r.Key)
	}
	// janela anterior tem o mesmo tamanho da atual
	r := parseRange("30d")
	cur := r.To.Sub(r.From)
	prev := r.PrevTo.Sub(r.PrevFrom)
	if (cur - prev).Abs() > 0 {
		t.Fatalf("janela anterior deveria ter o mesmo tamanho: cur=%v prev=%v", cur, prev)
	}
	if !r.PrevTo.Equal(r.From) {
		t.Fatalf("PrevTo deveria encostar em From")
	}
}

func TestBucketDatesUTC(t *testing.T) {
	loc := time.FixedZone("BRT", -3*3600)
	r := AnalyticsRange{
		Bucket: "day",
		From:   time.Date(2026, 6, 1, 10, 0, 0, 0, loc),
		To:     time.Date(2026, 6, 19, 22, 0, 0, 0, loc), // 2026-06-20 01:00 UTC
	}
	dates := bucketDates(r)
	if len(dates) == 0 {
		t.Fatal("vazio")
	}
	if dates[0] != "2026-06-01" {
		t.Fatalf("primeiro bucket: %q", dates[0])
	}
	// To em UTC é 2026-06-20 — o último bucket DEVE ser 2026-06-20 (senão a receita perto da meia-noite UTC some)
	if dates[len(dates)-1] != "2026-06-20" {
		t.Fatalf("último bucket deveria ser 2026-06-20 (UTC), veio %q", dates[len(dates)-1])
	}
}
