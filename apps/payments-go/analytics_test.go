package main

import "testing"

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
