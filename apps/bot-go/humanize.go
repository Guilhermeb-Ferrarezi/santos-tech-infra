package main

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// FirstBubbleDelayMs calcula o delay antes do primeiro balão.
// base=800ms, +50ms por palavra, +20ms por caractere, máx 4000ms.
func FirstBubbleDelayMs(text string) time.Duration {
	words := len(strings.Fields(text))
	chars := len(text)
	ms := 800 + words*50 + chars*20
	if ms > 4000 {
		ms = 4000
	}
	return time.Duration(ms) * time.Millisecond
}

// BetweenBubblesDelayMs calcula o delay entre balões consecutivos.
// base=400ms, +30ms por palavra, máx 2500ms.
func BetweenBubblesDelayMs(text string) time.Duration {
	words := len(strings.Fields(text))
	ms := 400 + words*30
	if ms > 2500 {
		ms = 2500
	}
	return time.Duration(ms) * time.Millisecond
}

// QuietHoursHoldMs retorna quantos milissegundos faltam para o término das quiet hours.
// Retorna 0 se now não estiver dentro do período de quiet hours.
// start e end são strings no formato "HH:MM", ex: "22:00", "08:00".
// Suporta períodos que cruzam meia-noite (ex: 22:00–08:00).
func QuietHoursHoldMs(now time.Time, timezone, start, end string) time.Duration {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		// timezone inválido: não aplica quiet hours
		return 0
	}

	local := now.In(loc)

	startH, startM := parseHHMM(start)
	endH, endM := parseHHMM(end)

	startOfDay := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	qStart := startOfDay.Add(time.Duration(startH)*time.Hour + time.Duration(startM)*time.Minute)
	qEnd := startOfDay.Add(time.Duration(endH)*time.Hour + time.Duration(endM)*time.Minute)

	// Período cruza meia-noite (ex: 22:00 → 08:00)
	crossesMidnight := !qEnd.After(qStart)

	var inQuiet bool
	var wakeAt time.Time

	if crossesMidnight {
		// Está em quiet hours se now >= qStart OU now < qEnd
		if !local.Before(qStart) {
			// now está na janela noturna (depois de qStart)
			// wakeAt = qEnd do dia seguinte
			inQuiet = true
			wakeAt = qEnd.Add(24 * time.Hour)
		} else if local.Before(qEnd) {
			// now está na madrugada (antes de qEnd, ainda em quiet hours)
			inQuiet = true
			wakeAt = qEnd
		}
	} else {
		// Período não cruza meia-noite (ex: 13:00 → 17:00)
		if !local.Before(qStart) && local.Before(qEnd) {
			inQuiet = true
			wakeAt = qEnd
		}
	}

	if !inQuiet {
		return 0
	}

	hold := wakeAt.Sub(local)
	if hold < 0 {
		return 0
	}
	return hold
}

// RandBetween retorna uma duração aleatória entre min e max (inclusive).
func RandBetween(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int63n(int64(max-min+1)))
}

// parseHHMM converte "HH:MM" em (horas, minutos). Retorna (0,0) em caso de erro.
func parseHHMM(s string) (int, int) {
	var h, m int
	_, err := fmt.Sscanf(s, "%d:%d", &h, &m)
	if err != nil {
		return 0, 0
	}
	return h, m
}
