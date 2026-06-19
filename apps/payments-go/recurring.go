package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

func monthlyDueDate(year int, month time.Month, day int) string {
	return fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)
}

// runRecurringLoop roda no boot: dispara já e depois 1x/dia.
func (s *Server) runRecurringLoop(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic em runRecurringLoop", "panic", rec)
		}
	}()
	s.generateMonthlyCharges(ctx) // roda no start (idempotente)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.generateMonthlyCharges(ctx)
		}
	}
}

func (s *Server) generateMonthlyCharges(ctx context.Context) {
	now := time.Now()
	day := now.Day()
	refMonth := fmt.Sprintf("%04d-%02d", now.Year(), int(now.Month()))
	rows, err := s.store.SubscriptionsDueToday(ctx, day, refMonth)
	if err != nil {
		slog.Error("recorrência: falha ao buscar assinaturas", "err", err)
		return
	}
	for _, row := range rows {
		ref := refMonth
		subID := row.Sub.ID
		student := row.Student
		c := &Charge{
			Kind: "mensalidade", SubscriptionID: &subID, StudentID: &student.ID,
			AmountCents: row.Sub.AmountCents, DueDate: monthlyDueDate(now.Year(), now.Month(), row.Sub.DueDay),
			ReferenceMonth: &ref, Provider: "efi", CorrelationID: newCorrelationID(),
		}
		if err := s.createAndPersistCharge(ctx, c, &student, "Mensalidade "+refMonth); err != nil {
			slog.Error("recorrência: falha ao gerar cobrança", "sub", subID, "err", err)
			continue
		}
		slog.Info("mensalidade gerada", "sub", subID, "charge", c.ID, "ref", refMonth)
	}
}
