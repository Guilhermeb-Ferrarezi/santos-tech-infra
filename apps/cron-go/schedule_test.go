package main

import (
	"testing"
	"time"
)

func TestNextRunDailySaoPaulo(t *testing.T) {
	// "todo dia às 09:00" em America/Sao_Paulo (UTC-3) = 12:00 UTC.
	after := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC) // 10:00 BRT — já passou das 9h
	got, err := nextRun("0 9 * * *", "America/Sao_Paulo", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC) // 9h BRT do dia seguinte
	if !got.Equal(want) {
		t.Fatalf("esperava %s, veio %s", want, got)
	}
}

func TestValidateCronRejectsGarbage(t *testing.T) {
	if err := validateCron("not a cron", "America/Sao_Paulo"); err == nil {
		t.Fatal("esperava erro para cron inválido")
	}
	if err := validateCron("0 9 * * *", "Mars/Phobos"); err == nil {
		t.Fatal("esperava erro para timezone inválida")
	}
}
