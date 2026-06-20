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

// nextDueDate devolve a próxima data FUTURA (após `from`) cujo dia do mês é dueDay.
// Usada como dataInicial do contrato quando a 1ª cobrança é "no dia do vencimento".
// dueDay é 1–28 (validado no produto), então não há overflow de dia em nenhum mês.
func nextDueDate(dueDay int, from time.Time) time.Time {
	y, m, _ := from.Date()
	cand := time.Date(y, m, dueDay, 0, 0, 0, 0, from.Location())
	if !cand.After(from) {
		cand = time.Date(y, m+1, dueDay, 0, 0, 0, 0, from.Location())
	}
	return cand
}

// nextCycleDate soma uma periodicidade a `from`. A Efí exige que a `dataInicial` do
// contrato (POST /v2/rec) seja FUTURA — não pode ser igual à data de criação. Na Jornada 3
// a 1ª parcela já é a cob imediata (hoje), então o contrato começa no próximo ciclo.
func nextCycleDate(periodicity string, from time.Time) time.Time {
	switch periodicity {
	case "SEMANAL":
		return from.AddDate(0, 0, 7)
	case "TRIMESTRAL":
		return from.AddDate(0, 3, 0)
	case "SEMESTRAL":
		return from.AddDate(0, 6, 0)
	case "ANUAL":
		return from.AddDate(1, 0, 0)
	default: // MENSAL e vazio
		return from.AddDate(0, 1, 0)
	}
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
		// O loop roda 1x/dia e gera no máximo 1 cobr/mês por recorrência (anti-duplicidade
		// por reference_month). Isso corresponde à periodicidade MENSAL. Periodicidades não
		// mensais (ex.: ANUAL) precisariam de outro intervalo entre ciclos — fora do escopo
		// desta fase — então o loop só cobra os ciclos seguintes das mensais (a 1ª parcela de
		// QUALQUER periodicidade já foi cobrada no checkout da Jornada 3).
		// TODO: suportar ciclos não mensais (intervalo por periodicidade) numa fase futura.
		if rec.Periodicity != "" && rec.Periodicity != "MENSAL" {
			continue
		}
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
