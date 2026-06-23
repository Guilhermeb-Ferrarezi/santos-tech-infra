package main

import (
	"testing"
	"time"
)

func TestBackoffGrows(t *testing.T) {
	if backoff(1) >= backoff(2) || backoff(2) >= backoff(3) {
		t.Fatal("backoff deveria crescer com a tentativa")
	}
	if backoff(10) > 5*time.Minute {
		t.Fatal("backoff deveria ter teto")
	}
}
