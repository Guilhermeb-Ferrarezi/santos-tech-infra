package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

func monthlyDueDate(year int, month time.Month, day int) string {
	return fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)
}

// recurringTxid produz um txid DETERMINÍSTICO por (recorrência, ciclo) para a cobr do PIX
// Automático. Ser determinístico torna o PUT /v2/cobr/{txid} idempotente na Efí: se o
// INSERT no banco falhar logo após o PUT bem-sucedido, a próxima execução diária refaz o
// PUT com o MESMO txid (a Efí atualiza a mesma cobr) em vez de criar um SEGUNDO débito
// automático para o mesmo ciclo. refMonth no formato YYYY-MM. Resultado: "stprec" + 19
// dígitos do id + YYYYMM = 31 chars alfanuméricos (txid Efí aceita 26–35).
func recurringTxid(recID int64, refMonth string) string {
	return fmt.Sprintf("stprec%019d%s", recID, strings.ReplaceAll(refMonth, "-", ""))
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

// runExpiryLoop marca cobranças pendentes vencidas como expiradas (QR já passou da
// validade na Efí). Roda no boot e depois 1x/hora — assim o dashboard não mantém
// cobranças "penduradas" em pending após o PIX expirar.
func (s *Server) runExpiryLoop(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic em runExpiryLoop", "panic", rec)
		}
	}()
	s.expireOverdueCharges(ctx) // roda no start
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.expireOverdueCharges(ctx)
		}
	}
}

// runRecurrenceCycleLoop roda no boot e depois 1x/dia: gera a cobr de cada ciclo de
// PIX Automático que vence hoje. Idempotente (anti-duplicidade por reference_month em
// RecurrencesDueToday). Mesmo padrão de runRecurringLoop.
func (s *Server) runRecurrenceCycleLoop(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic em runRecurrenceCycleLoop", "panic", rec)
		}
	}()
	s.generateRecurrenceCharges(ctx) // roda no start (idempotente)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.generateRecurrenceCharges(ctx)
		}
	}
}

// generateRecurrenceCharges cria a cobrança (cobr) de cada recorrência ativa cujo ciclo
// vence hoje, vinculada ao contrato via idRec. A cobr paga vira 'paid' pelo webhook pix
// existente (correlation_id = txid). Diferente da mensalidade simples: não usa o QR
// imediato — a Efí debita automaticamente no vencimento porque o pagador já autorizou.
func (s *Server) generateRecurrenceCharges(ctx context.Context) {
	now := time.Now()
	day := now.Day()
	refMonth := fmt.Sprintf("%04d-%02d", now.Year(), int(now.Month()))
	recs, err := s.store.RecurrencesDueToday(ctx, day, refMonth)
	if err != nil {
		slog.Error("recorrência: falha ao buscar contratos do ciclo", "err", err)
		return
	}
	for _, rec := range recs {
		ref := refMonth
		recID := rec.ID
		dueDate := monthlyDueDate(now.Year(), now.Month(), day)
		// O txid da cobr é o que geramos: a Efí o usa como id da cobrança (PUT /v2/cobr/{txid})
		// e o devolve no webhook pix do débito (resolvido por correlation_id em MarkChargePaid).
		// Determinístico por (rec, ciclo) → PUT idempotente, sem risco de débito duplicado.
		txid := recurringTxid(recID, ref)
		res, err := s.efi.CreateRecurringCharge(ctx, RecurringChargeRequest{
			CorrelationID: txid,
			EfiIDRec:      rec.EfiIDRec,
			AmountCents:   rec.AmountCents,
			PayerName:     rec.PayerName,
			PayerTaxID:    rec.PayerTaxID,
			DueDate:       dueDate,
		})
		if err != nil {
			slog.Error("recorrência: falha ao criar cobr do ciclo", "rec", recID, "err", err)
			continue
		}
		c := &Charge{
			RecurrenceID: &recID, CustomerID: rec.CustomerID,
			AmountCents: rec.AmountCents, DueDate: dueDate, ReferenceMonth: &ref,
			Provider: "efi", ProviderChargeID: res.ProviderChargeID,
			CorrelationID: txid, BRCode: res.BRCode, QRCode: res.QRCode,
		}
		c.payerTaxID = rec.PayerTaxID
		if err := s.store.InsertRecurrenceCharge(ctx, c); err != nil {
			slog.Error("recorrência: falha ao gravar cobrança do ciclo", "rec", recID, "err", err)
			continue
		}
		slog.Info("ciclo de recorrência gerado", "rec", recID, "charge", c.ID, "ref", refMonth)
	}
}

func (s *Server) expireOverdueCharges(ctx context.Context) {
	n, err := s.store.ExpireOverdueCharges(ctx)
	if err != nil {
		slog.Error("falha ao expirar cobranças vencidas", "err", err)
		return
	}
	if n > 0 {
		slog.Info("cobranças expiradas (QR vencido)", "count", n)
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
