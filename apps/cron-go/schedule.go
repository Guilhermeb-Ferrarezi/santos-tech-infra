package main

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// parser padrão de 5 campos (min hora dia mês dia-da-semana).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func nextRun(cronExpr, tz string, after time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, fmt.Errorf("timezone inválida: %w", err)
	}
	sched, err := cronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("cron inválido: %w", err)
	}
	// Calcula no fuso do job e devolve em UTC.
	next := sched.Next(after.In(loc))
	return next.UTC(), nil
}

func validateCron(cronExpr, tz string) error {
	_, err := nextRun(cronExpr, tz, time.Unix(0, 0).UTC())
	return err
}
