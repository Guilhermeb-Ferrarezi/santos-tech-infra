package main

import (
	"testing"
	"time"
)

// refNow: quarta-feira, 17/06/2026 14:00 BRT (fixo pra o teste ser determinístico).
var refNow = time.Date(2026, 6, 17, 14, 0, 0, 0, brLocation)

func TestParseClock(t *testing.T) {
	cases := []struct {
		in     string
		h, m   int
		wantOK bool
	}{
		{"19h30", 19, 30, true},
		{"19:30", 19, 30, true},
		{"19h", 19, 0, true},
		{"9h", 9, 0, true},
		{"7", 7, 0, true},
		{"08h05", 8, 5, true},
		{"20 horas", 20, 0, true},
		{"", 0, 0, false},
		{"abc", 0, 0, false},
		{"25h", 0, 0, false},
	}
	for _, c := range cases {
		h, m, ok := parseClock(c.in)
		if ok != c.wantOK || (ok && (h != c.h || m != c.m)) {
			t.Errorf("parseClock(%q) = (%d,%d,%v), quer (%d,%d,%v)", c.in, h, m, ok, c.h, c.m, c.wantOK)
		}
	}
}

func TestResolveBookingDateTime(t *testing.T) {
	cases := []struct {
		day, tm string
		want    string // RFC3339 esperado, ou "" se !ok
	}{
		// quarta é hoje (17/06) → próxima ocorrência = hoje
		{"quarta", "19h30", "2026-06-17T19:30:00-03:00"},
		// terça → próxima terça (23/06)
		{"terça", "9h", "2026-06-23T09:00:00-03:00"},
		{"ter", "9h", "2026-06-23T09:00:00-03:00"},
		// sexta → 19/06
		{"sexta-feira", "18:00", "2026-06-19T18:00:00-03:00"},
		{"amanhã", "10h", "2026-06-18T10:00:00-03:00"},
		{"hoje", "15h", "2026-06-17T15:00:00-03:00"},
		{"20/06", "14h", "2026-06-20T14:00:00-03:00"},
		{"2026-07-01", "8h", "2026-07-01T08:00:00-03:00"},
		// hora inválida → não resolve
		{"terça", "", ""},
		// dia ininteligível → não resolve
		{"qualquer", "19h", ""},
	}
	for _, c := range cases {
		got, ok := ResolveBookingDateTime(c.day, c.tm, refNow)
		if c.want == "" {
			if ok {
				t.Errorf("ResolveBookingDateTime(%q,%q) deveria falhar, veio %q", c.day, c.tm, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("ResolveBookingDateTime(%q,%q) = (%q,%v), quer %q", c.day, c.tm, got, ok, c.want)
		}
	}
}

func TestFormatBRDateTime(t *testing.T) {
	got := formatBRDateTime("2026-06-17T19:30:00-03:00")
	if got != "qua 17/06 às 19:30" {
		t.Errorf("formatBRDateTime = %q, quer %q", got, "qua 17/06 às 19:30")
	}
	// data pura (sem hora) também formata
	if got := formatBRDateTime("2026-06-19"); got == "" {
		t.Error("formatBRDateTime de data pura não deveria ser vazio")
	}
}
