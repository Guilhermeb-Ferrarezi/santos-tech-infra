package main

import (
	"testing"
	"time"
)

func TestMonthlyDueDate(t *testing.T) {
	if got := monthlyDueDate(2026, time.June, 10); got != "2026-06-10" {
		t.Fatalf("esperava 2026-06-10, veio %s", got)
	}
	if d := monthlyDueDate(2026, time.February, 28); d != "2026-02-28" {
		t.Fatalf("esperava 2026-02-28, veio %s", d)
	}
}
