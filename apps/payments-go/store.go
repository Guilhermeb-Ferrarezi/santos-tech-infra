package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	paydb "github.com/santos-tech/payments/db"
)

type Store struct {
	db *pgxpool.Pool
	q  *paydb.Queries
}

func newStore(db *pgxpool.Pool) *Store {
	return &Store{db: db, q: paydb.New(db)}
}

// tsToTime converte pgtype.Timestamptz para time.Time (zero se inválido).
func tsToTime(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}

// tsToTimePtr converte pgtype.Timestamptz para *time.Time (nil se inválido).
func tsToTimePtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

// ── Students ──────────────────────────────────────────────────────────────

func (s *Store) CreateStudent(ctx context.Context, st *Student) error {
	row, err := s.q.CreateStudent(ctx, paydb.CreateStudentParams{
		Name:  st.Name,
		TaxID: st.TaxID,
		Email: st.Email,
		Phone: st.Phone,
	})
	if err != nil {
		return err
	}
	st.ID = row.ID
	st.CreatedAt = tsToTime(row.CreatedAt)
	return nil
}

func (s *Store) ListStudents(ctx context.Context) ([]Student, error) {
	rows, err := s.q.ListStudents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Student, len(rows))
	for i, r := range rows {
		out[i] = Student{
			ID:        r.ID,
			Name:      r.Name,
			TaxID:     r.TaxID,
			Email:     r.Email,
			Phone:     r.Phone,
			CreatedAt: tsToTime(r.CreatedAt),
		}
	}
	return out, nil
}

func (s *Store) GetStudent(ctx context.Context, id int64) (*Student, error) {
	r, err := s.q.GetStudent(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Student{
		ID:        r.ID,
		Name:      r.Name,
		TaxID:     r.TaxID,
		Email:     r.Email,
		Phone:     r.Phone,
		CreatedAt: tsToTime(r.CreatedAt),
	}, nil
}

// ── Plans ─────────────────────────────────────────────────────────────────

func (s *Store) CreatePlan(ctx context.Context, p *Plan) error {
	row, err := s.q.CreatePlan(ctx, paydb.CreatePlanParams{
		Name:        p.Name,
		AmountCents: p.AmountCents,
		DueDay:      int32(p.DueDay),
	})
	if err != nil {
		return err
	}
	p.ID = row.ID
	p.Active = row.Active
	return nil
}

func (s *Store) ListPlans(ctx context.Context) ([]Plan, error) {
	rows, err := s.q.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Plan, len(rows))
	for i, r := range rows {
		out[i] = Plan{
			ID:          r.ID,
			Name:        r.Name,
			AmountCents: r.AmountCents,
			DueDay:      int(r.DueDay),
			Active:      r.Active,
		}
	}
	return out, nil
}

// ── Subscriptions ─────────────────────────────────────────────────────────

func (s *Store) CreateSubscription(ctx context.Context, sub *Subscription) error {
	row, err := s.q.CreateSubscription(ctx, paydb.CreateSubscriptionParams{
		StudentID:   sub.StudentID,
		PlanID:      sub.PlanID,
		AmountCents: sub.AmountCents,
		DueDay:      int32(sub.DueDay),
	})
	if err != nil {
		return err
	}
	sub.ID = row.ID
	sub.Status = row.Status
	return nil
}

func (s *Store) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := s.q.ListSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Subscription, len(rows))
	for i, r := range rows {
		out[i] = Subscription{
			ID:          r.ID,
			StudentID:   r.StudentID,
			PlanID:      r.PlanID,
			AmountCents: r.AmountCents,
			DueDay:      int(r.DueDay),
			Status:      r.Status,
		}
	}
	return out, nil
}

func (s *Store) SetSubscriptionStatus(ctx context.Context, id int64, status string) error {
	return s.q.SetSubscriptionStatus(ctx, paydb.SetSubscriptionStatusParams{
		ID:     id,
		Status: status,
	})
}

// SubDue agrupa uma assinatura a cobrar com o aluno correspondente (usado pela recorrência).
type SubDue struct {
	Sub     Subscription
	Student Student
}

// SubscriptionsDueToday: ativas cujo due_day == dia atual e sem charge no reference_month.
func (s *Store) SubscriptionsDueToday(ctx context.Context, day int, refMonth string) ([]SubDue, error) {
	rows, err := s.q.SubscriptionsDueToday(ctx, paydb.SubscriptionsDueTodayParams{
		DueDay:         int32(day),
		ReferenceMonth: &refMonth,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SubDue, len(rows))
	for i, r := range rows {
		out[i] = SubDue{
			Sub: Subscription{
				ID:          r.ID,
				StudentID:   r.StudentID,
				PlanID:      r.PlanID,
				AmountCents: r.AmountCents,
				DueDay:      int(r.DueDay),
				Status:      r.Status,
			},
			Student: Student{
				ID:        r.StudentID2,
				Name:      r.StudentName,
				TaxID:     r.StudentTaxID,
				Email:     r.StudentEmail,
				Phone:     r.StudentPhone,
				CreatedAt: tsToTime(r.StudentCreatedAt),
			},
		}
	}
	return out, nil
}

// ── Recurrences (PIX Automático) ──────────────────────────────────────────

// int32PtrFromInt converte *int → *int32 (nil preservado), p/ colunas INT opcionais.
func int32PtrFromInt(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

// intPtrFromInt32 é o inverso (coluna sqlc *int32 → *int do model).
func intPtrFromInt32(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

// optDateToPgtype converte string opcional "YYYY-MM-DD" para pgtype.Date (inválida se vazia).
func optDateToPgtype(dateStr string) pgtype.Date {
	if dateStr == "" {
		return pgtype.Date{}
	}
	return dateToPgtype(dateStr)
}

func (s *Store) CreateRecurrence(ctx context.Context, r *Recurrence) error {
	row, err := s.q.CreateRecurrence(ctx, paydb.CreateRecurrenceParams{
		SubscriptionID: r.SubscriptionID,
		ProductID:      r.ProductID,
		CustomerID:     r.CustomerID,
		PayerTaxID:     r.PayerTaxID,
		PayerName:      r.PayerName,
		AmountCents:    r.AmountCents,
		Periodicity:    r.Periodicity,
		DueDay:         int32PtrFromInt(r.DueDay),
		StartDate:      optDateToPgtype(r.StartDate),
		EndDate:        optDateToPgtype(r.EndDate),
		Journey:        int16(r.Journey),
		PublicToken:    nullStrPtr(r.PublicToken),
	})
	if err != nil {
		return err
	}
	r.ID = row.ID
	r.Status = row.Status
	r.CreatedAt = tsToTime(row.CreatedAt)
	return nil
}

// GetRecurrenceByPublicToken resolve a assinatura pelo token público (tela de checkout).
func (s *Store) GetRecurrenceByPublicToken(ctx context.Context, token string) (*Recurrence, error) {
	r, err := s.q.GetRecurrenceByPublicToken(ctx, &token)
	if err != nil {
		return nil, err
	}
	return &Recurrence{
		ID:             r.ID,
		SubscriptionID: r.SubscriptionID,
		ProductID:      r.ProductID,
		CustomerID:     r.CustomerID,
		PayerTaxID:     r.PayerTaxID,
		PayerName:      r.PayerName,
		AmountCents:    r.AmountCents,
		Periodicity:    r.Periodicity,
		DueDay:         intPtrFromInt32(r.DueDay),
		StartDate:      r.StartDate,
		EndDate:        r.EndDate,
		Journey:        int(r.Journey),
		EfiIDRec:       r.EfiIDRec,
		BRCode:         r.BrCode,
		QRCode:         r.QrCode,
		Status:         r.Status,
		PublicToken:    r.PublicToken,
		CreatedAt:      tsToTime(r.CreatedAt),
	}, nil
}

func (s *Store) GetRecurrence(ctx context.Context, id int64) (*Recurrence, error) {
	r, err := s.q.GetRecurrence(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Recurrence{
		ID:             r.ID,
		SubscriptionID: r.SubscriptionID,
		ProductID:      r.ProductID,
		CustomerID:     r.CustomerID,
		PayerTaxID:     r.PayerTaxID,
		PayerName:      r.PayerName,
		AmountCents:    r.AmountCents,
		Periodicity:    r.Periodicity,
		DueDay:         intPtrFromInt32(r.DueDay),
		StartDate:      r.StartDate,
		EndDate:        r.EndDate,
		Journey:        int(r.Journey),
		EfiIDRec:       r.EfiIDRec,
		BRCode:         r.BrCode,
		QRCode:         r.QrCode,
		Status:         r.Status,
		PublicToken:    r.PublicToken,
		CreatedAt:      tsToTime(r.CreatedAt),
	}, nil
}

func (s *Store) GetRecurrenceByEfiID(ctx context.Context, efiIDRec string) (*Recurrence, error) {
	r, err := s.q.GetRecurrenceByEfiID(ctx, &efiIDRec)
	if err != nil {
		return nil, err
	}
	return &Recurrence{
		ID:             r.ID,
		SubscriptionID: r.SubscriptionID,
		ProductID:      r.ProductID,
		CustomerID:     r.CustomerID,
		PayerTaxID:     r.PayerTaxID,
		PayerName:      r.PayerName,
		AmountCents:    r.AmountCents,
		Periodicity:    r.Periodicity,
		DueDay:         intPtrFromInt32(r.DueDay),
		StartDate:      r.StartDate,
		EndDate:        r.EndDate,
		Journey:        int(r.Journey),
		EfiIDRec:       r.EfiIDRec,
		BRCode:         r.BrCode,
		QRCode:         r.QrCode,
		Status:         r.Status,
		PublicToken:    r.PublicToken,
		CreatedAt:      tsToTime(r.CreatedAt),
	}, nil
}

func (s *Store) ListRecurrences(ctx context.Context) ([]Recurrence, error) {
	rows, err := s.q.ListRecurrences(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Recurrence, len(rows))
	for i, r := range rows {
		out[i] = Recurrence{
			ID:             r.ID,
			SubscriptionID: r.SubscriptionID,
			ProductID:      r.ProductID,
			CustomerID:     r.CustomerID,
			PayerTaxID:     r.PayerTaxID,
			PayerName:      r.PayerName,
			AmountCents:    r.AmountCents,
			Periodicity:    r.Periodicity,
			DueDay:         intPtrFromInt32(r.DueDay),
			StartDate:      r.StartDate,
			EndDate:        r.EndDate,
			Journey:        int(r.Journey),
			EfiIDRec:       r.EfiIDRec,
			BRCode:         r.BrCode,
			QRCode:         r.QrCode,
			Status:         r.Status,
			PublicToken:    r.PublicToken,
			CreatedAt:      tsToTime(r.CreatedAt),
		}
	}
	return out, nil
}

// ListRecurrencesByUserID lista as recorrências (PIX Automático) do usuário logado,
// amarradas pelo customer_id. Filtrar por CPF (ListRecurrencesByTaxID) NÃO serve aqui:
// o CPF não prova posse da conta e vazava assinaturas de terceiros.
func (s *Store) ListRecurrencesByUserID(ctx context.Context, userID int64) ([]Recurrence, error) {
	rows, err := s.q.ListRecurrencesByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Recurrence, len(rows))
	for i, r := range rows {
		out[i] = Recurrence{
			ID:             r.ID,
			SubscriptionID: r.SubscriptionID,
			ProductID:      r.ProductID,
			CustomerID:     r.CustomerID,
			PayerTaxID:     r.PayerTaxID,
			PayerName:      r.PayerName,
			AmountCents:    r.AmountCents,
			Periodicity:    r.Periodicity,
			DueDay:         intPtrFromInt32(r.DueDay),
			StartDate:      r.StartDate,
			EndDate:        r.EndDate,
			Journey:        int(r.Journey),
			EfiIDRec:       r.EfiIDRec,
			BRCode:         r.BrCode,
			QRCode:         r.QrCode,
			Status:         r.Status,
			PublicToken:    r.PublicToken,
			CreatedAt:      tsToTime(r.CreatedAt),
		}
	}
	return out, nil
}

// ListRecurrencesByTaxID lista as recorrências (PIX Automático) de um cliente pelo CPF.
func (s *Store) ListRecurrencesByTaxID(ctx context.Context, taxID string) ([]Recurrence, error) {
	rows, err := s.q.ListRecurrencesByTaxID(ctx, taxID)
	if err != nil {
		return nil, err
	}
	out := make([]Recurrence, len(rows))
	for i, r := range rows {
		out[i] = Recurrence{
			ID:             r.ID,
			SubscriptionID: r.SubscriptionID,
			ProductID:      r.ProductID,
			CustomerID:     r.CustomerID,
			PayerTaxID:     r.PayerTaxID,
			PayerName:      r.PayerName,
			AmountCents:    r.AmountCents,
			Periodicity:    r.Periodicity,
			DueDay:         intPtrFromInt32(r.DueDay),
			StartDate:      r.StartDate,
			EndDate:        r.EndDate,
			Journey:        int(r.Journey),
			EfiIDRec:       r.EfiIDRec,
			BRCode:         r.BrCode,
			QRCode:         r.QrCode,
			Status:         r.Status,
			PublicToken:    r.PublicToken,
			CreatedAt:      tsToTime(r.CreatedAt),
		}
	}
	return out, nil
}

func (s *Store) SetRecurrenceStatus(ctx context.Context, id int64, status string) error {
	return s.q.SetRecurrenceStatus(ctx, paydb.SetRecurrenceStatusParams{
		ID:     id,
		Status: status,
	})
}

// UpdateRecurrenceAuth grava o resultado da criação na Efí (idRec + QR/copia-e-cola) e o status.
func (s *Store) UpdateRecurrenceAuth(ctx context.Context, id int64, efiIDRec, brCode, qrCode, status string) error {
	return s.q.UpdateRecurrenceAuth(ctx, paydb.UpdateRecurrenceAuthParams{
		ID:       id,
		EfiIDRec: nullStrPtr(efiIDRec),
		BrCode:   nullStrPtr(brCode),
		QrCode:   nullStrPtr(qrCode),
		Status:   status,
	})
}

// RecurrencesDueToday: 'active' cujo due_day == dia atual, dentro da vigência, e sem
// cobrança no reference_month do ciclo (anti-duplicidade, espelha SubscriptionsDueToday).
func (s *Store) RecurrencesDueToday(ctx context.Context, day int, refMonth string) ([]Recurrence, error) {
	rows, err := s.q.RecurrencesDueToday(ctx, paydb.RecurrencesDueTodayParams{
		DueDay:         int32PtrFromInt(&day),
		ReferenceMonth: &refMonth,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Recurrence, len(rows))
	for i, r := range rows {
		out[i] = Recurrence{
			ID:             r.ID,
			SubscriptionID: r.SubscriptionID,
			ProductID:      r.ProductID,
			CustomerID:     r.CustomerID,
			PayerTaxID:     r.PayerTaxID,
			PayerName:      r.PayerName,
			AmountCents:    r.AmountCents,
			Periodicity:    r.Periodicity,
			DueDay:         intPtrFromInt32(r.DueDay),
			StartDate:      r.StartDate,
			EndDate:        r.EndDate,
			Journey:        int(r.Journey),
			EfiIDRec:       r.EfiIDRec,
			BRCode:         r.BrCode,
			QRCode:         r.QrCode,
			Status:         r.Status,
			PublicToken:    r.PublicToken,
			CreatedAt:      tsToTime(r.CreatedAt),
		}
	}
	return out, nil
}

// InsertRecurrenceCharge cria a pay_charges de um ciclo (kind='recorrente'), vinculada
// à recorrência. provider_charge_id guarda o txid da cobr da Efí.
func (s *Store) InsertRecurrenceCharge(ctx context.Context, c *Charge) error {
	row, err := s.q.InsertRecurrenceCharge(ctx, paydb.InsertRecurrenceChargeParams{
		RecurrenceID:     c.RecurrenceID,
		CustomerID:       c.CustomerID,
		AmountCents:      c.AmountCents,
		DueDate:          dateToPgtype(c.DueDate),
		ReferenceMonth:   c.ReferenceMonth,
		Provider:         c.Provider,
		ProviderChargeID: nullStrPtr(c.ProviderChargeID),
		CorrelationID:    c.CorrelationID,
		PayerTaxID:       nullStrPtr(c.payerTaxID),
		BrCode:           nullStrPtr(c.BRCode),
		QrCode:           nullStrPtr(c.QRCode),
		PublicToken:      nullStrPtr(c.PublicToken),
	})
	if err != nil {
		return err
	}
	c.ID = row.ID
	c.Status = row.Status
	c.CreatedAt = tsToTime(row.CreatedAt)
	return nil
}

// ── Charges ───────────────────────────────────────────────────────────────

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullStrPtr converte string para *string (nil se vazia).
func nullStrPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// dateToPgtype converte string "YYYY-MM-DD" para pgtype.Date.
func dateToPgtype(dateStr string) pgtype.Date {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

// methodOrPix normaliza o método: vazio (cobranças antigas/PIX) → "pix".
func methodOrPix(m string) string {
	if m == "" {
		return "pix"
	}
	return m
}

func (s *Store) InsertCharge(ctx context.Context, c *Charge) error {
	row, err := s.q.InsertCharge(ctx, paydb.InsertChargeParams{
		Kind:             c.Kind,
		SubscriptionID:   c.SubscriptionID,
		StudentID:        c.StudentID,
		CustomerID:       c.CustomerID,
		AmountCents:      c.AmountCents,
		DueDate:          dateToPgtype(c.DueDate),
		ReferenceMonth:   c.ReferenceMonth,
		Provider:         c.Provider,
		ProviderChargeID: nullStrPtr(c.ProviderChargeID),
		CorrelationID:    c.CorrelationID,
		PublicToken:      nullStrPtr(c.PublicToken),
		PayerTaxID:       nullStrPtr(c.payerTaxID),
		Method:           methodOrPix(c.Method),
		BrCode:           nullStrPtr(c.BRCode),
		QrCode:           nullStrPtr(c.QRCode),
		PdfUrl:           nullStrPtr(c.PDFURL),
		Barcode:          nullStrPtr(c.Barcode),
	})
	if err != nil {
		return err
	}
	c.ID = row.ID
	c.Status = row.Status
	c.CreatedAt = tsToTime(row.CreatedAt)
	return nil
}

// ListCharges filtra opcionalmente por status e studentID.
// Não migrada para sqlc: usa SQL com filtros condicionais inline ($1=” e $2=0
// significam "sem filtro"), o que não é suportado estaticamente pelo sqlc.
func (s *Store) ListCharges(ctx context.Context, status string, studentID int64) ([]Charge, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.id, c.kind, c.subscription_id, c.student_id, c.amount_cents, c.due_date::text, c.reference_month,
		       c.status, c.provider, COALESCE(c.provider_charge_id,''), c.correlation_id, c.method,
		       COALESCE(c.br_code,''), COALESCE(c.qr_code,''), c.paid_at, c.created_at,
		       COALESCE(NULLIF(st.name,''), NULLIF(cu.name,''), '') AS payer_name,
		       COALESCE(NULLIF(st.email,''), NULLIF(cu.email,''), '') AS payer_email
		FROM pay_charges c
		LEFT JOIN pay_students st ON st.id = c.student_id
		LEFT JOIN pay_customers cu ON cu.id = c.customer_id
		WHERE ($1='' OR c.status=$1) AND ($2=0 OR c.student_id=$2)
		ORDER BY c.created_at DESC`, status, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Charge{}
	for rows.Next() {
		var c Charge
		if err := rows.Scan(&c.ID, &c.Kind, &c.SubscriptionID, &c.StudentID, &c.AmountCents, &c.DueDate,
			&c.ReferenceMonth, &c.Status, &c.Provider, &c.ProviderChargeID, &c.CorrelationID, &c.Method,
			&c.BRCode, &c.QRCode, &c.PaidAt, &c.CreatedAt, &c.PayerName, &c.PayerEmail); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetStats usa SQL com fragmento dinâmico (cláusula WHERE montada em runtime).
// Não migrada para sqlc: duas chamadas para o mesmo scan com WHERE diferente.
func (s *Store) GetStats(ctx context.Context) (StatsResult, error) {
	var r StatsResult
	scan := func(p *StatsPeriod, where string) error {
		return s.db.QueryRow(ctx, `
			SELECT
				COALESCE(COUNT(*) FILTER (WHERE status='paid'),     0),
				COALESCE(SUM(amount_cents) FILTER (WHERE status='paid'), 0),
				COALESCE(COUNT(*) FILTER (WHERE status='pending'),  0),
				COALESCE(SUM(amount_cents) FILTER (WHERE status='pending'), 0),
				COALESCE(COUNT(*) FILTER (WHERE status='expired'),  0),
				COALESCE(COUNT(*) FILTER (WHERE status='canceled'), 0)
			FROM pay_charges `+where).
			Scan(&p.PaidCount, &p.PaidTotal, &p.PendingCount, &p.PendingTotal, &p.ExpiredCount, &p.CanceledCount)
	}
	if err := scan(&r.Month, "WHERE created_at >= date_trunc('month', now())"); err != nil {
		return r, err
	}
	if err := scan(&r.AllTime, ""); err != nil {
		return r, err
	}
	return r, nil
}

func (s *Store) GetCharge(ctx context.Context, id int64) (*Charge, error) {
	r, err := s.q.GetCharge(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Charge{
		ID:               r.ID,
		Kind:             r.Kind,
		SubscriptionID:   r.SubscriptionID,
		StudentID:        r.StudentID,
		AmountCents:      r.AmountCents,
		DueDate:          r.DueDate,
		ReferenceMonth:   r.ReferenceMonth,
		Status:           r.Status,
		Provider:         r.Provider,
		ProviderChargeID: r.ProviderChargeID,
		CorrelationID:    r.CorrelationID,
		Method:           r.Method,
		BRCode:           r.BrCode,
		QRCode:           r.QrCode,
		PDFURL:           r.PdfUrl,
		Barcode:          r.Barcode,
		PaidAt:           tsToTimePtr(r.PaidAt),
		CreatedAt:        tsToTime(r.CreatedAt),
	}, nil
}

func (s *Store) MarkChargePaid(ctx context.Context, correlationID string) error {
	return s.q.MarkChargePaid(ctx, correlationID)
}

func (s *Store) MarkChargeExpired(ctx context.Context, correlationID string) error {
	return s.q.MarkChargeExpired(ctx, correlationID)
}

// MarkChargeRefunded marca a cobrança como estornada (status='refunded') pelo
// correlationID. Fecha a janela de double-refund: uma 2ª chamada de RefundCard
// é bloqueada antes de chegar ao gateway (o handler verifica c.Status == "refunded").
// O status 'refunded' não está no CHECK atual de pay_charges — adicionamos via migration.
func (s *Store) MarkChargeRefunded(ctx context.Context, correlationID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE pay_charges SET status='refunded' WHERE correlation_id=$1 AND status='paid'`,
		correlationID)
	return err
}

// ExpireOverdueCharges marca como expiradas as cobranças pendentes vencidas (job
// periódico). Devolve quantas foram expiradas.
func (s *Store) ExpireOverdueCharges(ctx context.Context) (int64, error) {
	return s.q.ExpireOverdueCharges(ctx)
}

// CancelChargeByToken cancela uma cobrança pendente pelo token público (tela de
// pagamento). Devolve correlationID e providerChargeID para cancelar no gateway.
// pgx.ErrNoRows se não havia cobrança pendente com esse token.
func (s *Store) CancelChargeByToken(ctx context.Context, token string) (correlationID, providerChargeID, method string, err error) {
	r, err := s.q.CancelChargeByToken(ctx, &token)
	if err != nil {
		return "", "", "", err
	}
	return r.CorrelationID, r.ProviderChargeID, r.Method, nil
}

// MarkChargePaidByProviderID marca paga a cobrança casada pelo charge_id do Efí
// (boleto/API Cobranças). Devolve true se realmente passou de pending→paid agora.
func (s *Store) MarkChargePaidByProviderID(ctx context.Context, providerChargeID string) (bool, error) {
	n, err := s.q.MarkChargePaidByProviderID(ctx, &providerChargeID)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *Store) PublicTokenByProviderID(ctx context.Context, providerChargeID string) (string, error) {
	tok, err := s.q.PublicTokenByProviderID(ctx, &providerChargeID)
	if err != nil || tok == nil {
		return "", err
	}
	return *tok, nil
}

// ── Webhook idempotência ──────────────────────────────────────────────────

// ClaimWebhookEvent reivindica o evento (fase 1) e devolve true se este processo
// deve aplicar o efeito. O evento fica 'pending' — só MarkWebhookDone o encerra.
//
// NÃO substitua isto por um "marcar visto" antes do efeito: era exatamente esse o
// bug — um timeout da Efí ou erro de banco no meio do caminho descartava o
// pagamento para sempre, porque a reentrega caía no "já visto".
func (s *Store) ClaimWebhookEvent(ctx context.Context, id, typ string, payload []byte) (bool, error) {
	n, err := s.q.ClaimWebhookEvent(ctx, paydb.ClaimWebhookEventParams{
		ID:      id,
		Type:    typ,
		Payload: payload,
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// MarkWebhookDone encerra o evento (fase 2), depois do efeito aplicado.
func (s *Store) MarkWebhookDone(ctx context.Context, id string) error {
	return s.q.MarkWebhookDone(ctx, id)
}

func (s *Store) PublicTokenByCorrelation(ctx context.Context, correlationID string) (string, error) {
	tok, err := s.q.PublicTokenByCorrelation(ctx, correlationID)
	if err != nil || tok == nil {
		return "", err
	}
	return *tok, nil
}

// ── Products ──────────────────────────────────────────────────────────────

func (s *Store) CreateProduct(ctx context.Context, p *Product) error {
	row, err := s.q.CreateProduct(ctx, paydb.CreateProductParams{
		Slug:              p.Slug,
		Name:              p.Name,
		Description:       p.Description,
		PriceCents:        p.PriceCents,
		Recurring:         p.Recurring,
		Periodicity:       nullStrPtr(p.Periodicity),
		DueDay:            int32PtrFromInt(p.DueDay),
		ChargeOnSubscribe: p.ChargeOnSubscribe,
		ImageUrl:          nullStrPtr(p.ImageURL),
		FileUrl:           nullStrPtr(p.FileURL),
	})
	if err != nil {
		return err
	}
	p.ID = row.ID
	p.Active = row.Active
	p.ImageURL = row.ImageUrl
	p.FileURL = row.FileUrl
	return nil
}

// strPtrToStr converte *string (coluna TEXT opcional do sqlc) para string ("" se nil).
func strPtrToStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Store) ListProducts(ctx context.Context) ([]Product, error) {
	rows, err := s.q.ListProducts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Product, len(rows))
	for i, r := range rows {
		out[i] = Product{
			ID:                r.ID,
			Slug:              r.Slug,
			Name:              r.Name,
			Description:       r.Description,
			PriceCents:        r.PriceCents,
			Active:            r.Active,
			Recurring:         r.Recurring,
			Periodicity:       strPtrToStr(r.Periodicity),
			DueDay:            intPtrFromInt32(r.DueDay),
			ChargeOnSubscribe: r.ChargeOnSubscribe,
			ImageURL:          r.ImageUrl,
			FileURL:           r.FileUrl,
		}
	}
	return out, nil
}

func (s *Store) GetProductBySlug(ctx context.Context, slug string) (*Product, error) {
	r, err := s.q.GetProductBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	return &Product{
		ID:                r.ID,
		Slug:              r.Slug,
		Name:              r.Name,
		Description:       r.Description,
		PriceCents:        r.PriceCents,
		Active:            r.Active,
		Recurring:         r.Recurring,
		Periodicity:       strPtrToStr(r.Periodicity),
		DueDay:            intPtrFromInt32(r.DueDay),
		ChargeOnSubscribe: r.ChargeOnSubscribe,
		ImageURL:          r.ImageUrl,
		FileURL:           r.FileUrl,
	}, nil
}

func (s *Store) GetProductByID(ctx context.Context, id int64) (*Product, error) {
	r, err := s.q.GetProductByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Product{
		ID:                r.ID,
		Slug:              r.Slug,
		Name:              r.Name,
		Description:       r.Description,
		PriceCents:        r.PriceCents,
		Active:            r.Active,
		Recurring:         r.Recurring,
		Periodicity:       strPtrToStr(r.Periodicity),
		DueDay:            intPtrFromInt32(r.DueDay),
		ChargeOnSubscribe: r.ChargeOnSubscribe,
		ImageURL:          r.ImageUrl,
		FileURL:           r.FileUrl,
	}, nil
}

func (s *Store) DeleteProduct(ctx context.Context, id int64) error {
	n, err := s.q.DeleteProduct(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateProduct(ctx context.Context, p *Product) error {
	n, err := s.q.UpdateProduct(ctx, paydb.UpdateProductParams{
		ID:                p.ID,
		Name:              p.Name,
		Description:       p.Description,
		PriceCents:        p.PriceCents,
		Active:            p.Active,
		Recurring:         p.Recurring,
		Periodicity:       nullStrPtr(p.Periodicity),
		DueDay:            int32PtrFromInt(p.DueDay),
		ChargeOnSubscribe: p.ChargeOnSubscribe,
		ImageUrl:          nullStrPtr(p.ImageURL),
		FileUrl:           nullStrPtr(p.FileURL),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ── Customers ─────────────────────────────────────────────────────────────

func (s *Store) UpsertCustomer(ctx context.Context, userID int64, taxID, phone, name, email string) (*Customer, error) {
	r, err := s.q.UpsertCustomer(ctx, paydb.UpsertCustomerParams{
		UserID: userID,
		TaxID:  taxID,
		Phone:  phone,
		Name:   name,
		Email:  email,
	})
	if err != nil {
		return nil, err
	}
	return &Customer{
		ID:     r.ID,
		UserID: r.UserID,
		TaxID:  r.TaxID,
		Phone:  r.Phone,
		Name:   r.Name,
		Email:  r.Email,
	}, nil
}

func (s *Store) GetCustomerByUserID(ctx context.Context, userID int64) (*Customer, error) {
	r, err := s.q.GetCustomerByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &Customer{
		ID:     r.ID,
		UserID: r.UserID,
		TaxID:  r.TaxID,
		Phone:  r.Phone,
		Name:   r.Name,
		Email:  r.Email,
	}, nil
}

// GetCustomerByTaxID devolve o cliente mais recente com aquele CPF (qualquer conta),
// ou pgx.ErrNoRows se nenhum existir. Usado pelo "Gerar PIX" avulso.
func (s *Store) GetCustomerByTaxID(ctx context.Context, taxID string) (*Customer, error) {
	r, err := s.q.GetCustomerByTaxID(ctx, taxID)
	if err != nil {
		return nil, err
	}
	return &Customer{
		ID:     r.ID,
		UserID: r.UserID,
		TaxID:  r.TaxID,
		Phone:  r.Phone,
		Name:   r.Name,
		Email:  r.Email,
	}, nil
}

// ListCustomersWithStats lista os clientes com agregados das compras (admin).
func (s *Store) ListCustomersWithStats(ctx context.Context) ([]CustomerWithStats, error) {
	rows, err := s.q.ListCustomersWithStats(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CustomerWithStats, len(rows))
	for i, r := range rows {
		out[i] = CustomerWithStats{
			ID:             r.ID,
			UserID:         r.UserID,
			TaxID:          r.TaxID,
			Phone:          r.Phone,
			Name:           r.Name,
			Email:          r.Email,
			CreatedAt:      tsToTime(r.CreatedAt),
			ChargesCount:   r.ChargesCount,
			PaidCount:      r.PaidCount,
			PaidTotalCents: r.PaidTotalCents,
			LastChargeAt:   tsToTimePtr(r.LastChargeAt),
		}
	}
	return out, nil
}

// GetCustomerDetail devolve o cliente e o histórico de compras (cobranças + itens).
func (s *Store) GetCustomerDetail(ctx context.Context, id int64) (*CustomerDetail, error) {
	cu, err := s.q.GetCustomerByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// Consolida por CPF: todas as compras de QUALQUER conta com o mesmo tax_id —
	// assim o detalhe bate com a lista (que agrupa clientes por CPF).
	charges, err := s.q.ListChargesByTaxID(ctx, cu.TaxID)
	if err != nil {
		return nil, err
	}
	items, err := s.q.ListChargeItemsByTaxID(ctx, cu.TaxID)
	if err != nil {
		return nil, err
	}
	byCharge := make(map[int64][]ChargeItem, len(items))
	for _, it := range items {
		byCharge[it.ChargeID] = append(byCharge[it.ChargeID], ChargeItem{
			ProductID:  it.ProductID,
			Name:       it.Name,
			PriceCents: it.PriceCents,
			Quantity:   int(it.Quantity),
		})
	}
	purchases := make([]CustomerCharge, len(charges))
	for i, c := range charges {
		its := byCharge[c.ID]
		if its == nil {
			its = []ChargeItem{}
		}
		purchases[i] = CustomerCharge{
			ID:            c.ID,
			Kind:          c.Kind,
			AmountCents:   c.AmountCents,
			Status:        c.Status,
			DueDate:       c.CDueDate,
			CorrelationID: c.CorrelationID,
			PaidAt:        tsToTimePtr(c.PaidAt),
			CreatedAt:     tsToTime(c.CreatedAt),
			Items:         its,
		}
	}
	// Assinaturas (PIX Automático) do cliente, consolidadas pelo mesmo CPF das compras.
	recs, err := s.ListRecurrencesByTaxID(ctx, cu.TaxID)
	if err != nil {
		return nil, err
	}
	return &CustomerDetail{
		ID:          cu.ID,
		UserID:      cu.UserID,
		TaxID:       cu.TaxID,
		Phone:       cu.Phone,
		Name:        cu.Name,
		Email:       cu.Email,
		CreatedAt:   tsToTime(cu.CreatedAt),
		Charges:     purchases,
		Recurrences: recs,
	}, nil
}

// ── Charge items + charges por token/cliente ──────────────────────────────

func (s *Store) InsertChargeItems(ctx context.Context, chargeID int64, items []ChargeItem) error {
	for _, it := range items {
		if err := s.q.InsertChargeItem(ctx, paydb.InsertChargeItemParams{
			ChargeID:   chargeID,
			ProductID:  it.ProductID,
			Name:       it.Name,
			PriceCents: it.PriceCents,
			Quantity:   int32(it.Quantity),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetChargeByPublicToken(ctx context.Context, token string) (*Charge, error) {
	r, err := s.q.GetChargeByPublicToken(ctx, &token)
	if err != nil {
		return nil, err
	}
	return &Charge{
		ID:               r.ID,
		Kind:             r.Kind,
		StudentID:        r.StudentID,
		CustomerID:       r.CustomerID,
		AmountCents:      r.AmountCents,
		DueDate:          r.DueDate,
		Status:           r.Status,
		Method:           r.Method,
		BRCode:           r.BrCode,
		QRCode:           r.QrCode,
		PDFURL:           r.PdfUrl,
		Barcode:          r.Barcode,
		ProviderChargeID: r.ProviderChargeID,
		CorrelationID:    r.CorrelationID,
		PaidAt:           tsToTimePtr(r.PaidAt),
		CreatedAt:        tsToTime(r.CreatedAt),
	}, nil
}

func (s *Store) PayerEmailByCharge(ctx context.Context, chargeID int64) (name, email string, err error) {
	r, err := s.q.PayerEmailByCharge(ctx, chargeID)
	if err != nil {
		return "", "", err
	}
	return r.Name, r.Email, nil
}

func (s *Store) ListChargesByCustomer(ctx context.Context, customerID int64) ([]Charge, error) {
	rows, err := s.q.ListChargesByCustomer(ctx, &customerID)
	if err != nil {
		return nil, err
	}
	out := make([]Charge, len(rows))
	for i, r := range rows {
		out[i] = Charge{
			ID:            r.ID,
			Kind:          r.Kind,
			AmountCents:   r.AmountCents,
			DueDate:       r.DueDate,
			Status:        r.Status,
			BRCode:        r.BrCode,
			CorrelationID: r.CorrelationID,
			PaidAt:        tsToTimePtr(r.PaidAt),
			CreatedAt:     tsToTime(r.CreatedAt),
		}
	}
	return out, nil
}

// ListChargesByRecurrence lista os ciclos (cobranças) de uma recorrência de PIX Automático.
func (s *Store) ListChargesByRecurrence(ctx context.Context, recurrenceID int64) ([]Charge, error) {
	rows, err := s.q.ListChargesByRecurrence(ctx, &recurrenceID)
	if err != nil {
		return nil, err
	}
	out := make([]Charge, len(rows))
	for i, r := range rows {
		out[i] = Charge{
			ID:             r.ID,
			Kind:           r.Kind,
			AmountCents:    r.AmountCents,
			DueDate:        r.DueDate,
			ReferenceMonth: r.ReferenceMonth,
			Status:         r.Status,
			BRCode:         r.BrCode,
			CorrelationID:  r.CorrelationID,
			PaidAt:         tsToTimePtr(r.PaidAt),
			CreatedAt:      tsToTime(r.CreatedAt),
		}
	}
	return out, nil
}

// GetAnalytics agrega os dados do dashboard num único batch. Tudo sobre pay_charges,
// mais um COUNT de assinaturas ativas. Datas: "recebido" por paid_at, "emitido" por created_at.
func (s *Store) GetAnalytics(ctx context.Context, r AnalyticsRange) (Analytics, error) {
	out := Analytics{Range: r.Key}

	// KPIs num único SELECT.
	err := s.db.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(amount_cents) FILTER (WHERE status='paid' AND paid_at >= $1 AND paid_at < $2), 0),
			COALESCE(COUNT(*)          FILTER (WHERE status='paid' AND paid_at >= $1 AND paid_at < $2), 0),
			COALESCE(SUM(amount_cents) FILTER (WHERE status='pending'), 0),
			COALESCE(COUNT(*)          FILTER (WHERE status='pending'), 0),
			COALESCE(SUM(amount_cents) FILTER (WHERE status='pending' AND due_date < current_date), 0),
			COALESCE(COUNT(*)          FILTER (WHERE status='pending' AND due_date < current_date), 0),
			COALESCE(COUNT(*)          FILTER (WHERE status='expired'  AND created_at >= $1 AND created_at < $2), 0),
			COALESCE(COUNT(*)          FILTER (WHERE status='canceled' AND created_at >= $1 AND created_at < $2), 0),
			COALESCE(COUNT(*)          FILTER (WHERE created_at >= $1 AND created_at < $2), 0),
			COALESCE(SUM(amount_cents) FILTER (WHERE status='paid' AND paid_at >= $3 AND paid_at < $4), 0)
		FROM pay_charges`,
		r.From, r.To, r.PrevFrom, r.PrevTo).
		Scan(&out.KPIs.PaidTotal, &out.KPIs.PaidCount, &out.KPIs.PendingTotal, &out.KPIs.PendingCount,
			&out.KPIs.OverdueTotal, &out.KPIs.OverdueCount, &out.KPIs.ExpiredCount, &out.KPIs.CanceledCount,
			&out.KPIs.emittedCount, &out.KPIs.prevPaidTotal)
	if err != nil {
		return out, err
	}
	if out.KPIs.PaidCount > 0 {
		out.KPIs.AvgTicketCents = out.KPIs.PaidTotal / out.KPIs.PaidCount
	}
	if out.KPIs.emittedCount > 0 {
		out.KPIs.ConversionRate = float64(out.KPIs.PaidCount) / float64(out.KPIs.emittedCount)
	}
	if out.KPIs.prevPaidTotal > 0 {
		out.KPIs.DeltaPaidPct = float64(out.KPIs.PaidTotal-out.KPIs.prevPaidTotal) / float64(out.KPIs.prevPaidTotal)
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM pay_subscriptions WHERE status='active'`).Scan(&out.KPIs.ActiveSubscriptions); err != nil {
		slog.Warn("analytics: falha ao contar assinaturas ativas", "err", err)
	}

	// Timeseries: DUAS agregações independentes (recebido por paid_at, emitido por
	// created_at), merjadas em Go sobre a lista de buckets. Evita o bug de uma cobrança
	// criada num bucket e paga noutro ser contada no "pago" do bucket errado.
	truncSQL := "day"
	if r.Bucket == "month" {
		truncSQL = "month"
	}
	paidByB, err := s.bucketSeries(ctx, `
		SELECT to_char(date_trunc($3, paid_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), COALESCE(SUM(amount_cents),0), COUNT(*)
		FROM pay_charges WHERE status='paid' AND paid_at >= $1 AND paid_at < $2 GROUP BY 1`, r.From, r.To, truncSQL)
	if err != nil {
		return out, err
	}
	emitByB, err := s.bucketSeries(ctx, `
		SELECT to_char(date_trunc($3, created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), COALESCE(SUM(amount_cents),0), COUNT(*)
		FROM pay_charges WHERE created_at >= $1 AND created_at < $2 GROUP BY 1`, r.From, r.To, truncSQL)
	if err != nil {
		return out, err
	}
	for _, d := range bucketDates(r) {
		p, e := paidByB[d], emitByB[d]
		out.Timeseries = append(out.Timeseries, TimePoint{
			Date: d, PaidTotal: p.Total, PaidCount: p.Count, EmittedTotal: e.Total, EmittedCount: e.Count,
		})
	}

	// byStatus (no período, por created_at).
	out.ByStatus, err = s.bucketQuery(ctx, `
		SELECT status, COALESCE(SUM(amount_cents),0), COUNT(*)
		FROM pay_charges WHERE created_at >= $1 AND created_at < $2 GROUP BY status`, r.From, r.To)
	if err != nil {
		return out, err
	}
	// byKind (receita paga no período).
	out.ByKind, err = s.bucketQuery(ctx, `
		SELECT kind, COALESCE(SUM(amount_cents),0), COUNT(*)
		FROM pay_charges WHERE status='paid' AND paid_at >= $1 AND paid_at < $2 GROUP BY kind`, r.From, r.To)
	if err != nil {
		return out, err
	}
	// topProducts (itens de cobranças pagas no período, top 5 por receita).
	out.TopProducts, err = s.bucketQuery(ctx, `
		SELECT i.name, COALESCE(SUM(i.price_cents*i.quantity),0), COUNT(*)
		FROM pay_charge_items i JOIN pay_charges c ON c.id=i.charge_id
		WHERE c.status='paid' AND c.paid_at >= $1 AND c.paid_at < $2
		GROUP BY i.name ORDER BY 2 DESC LIMIT 5`, r.From, r.To)
	if err != nil {
		return out, err
	}
	if out.Timeseries == nil {
		out.Timeseries = []TimePoint{}
	}
	if out.ByStatus == nil {
		out.ByStatus = []Bucket{}
	}
	if out.ByKind == nil {
		out.ByKind = []Bucket{}
	}
	if out.TopProducts == nil {
		out.TopProducts = []Bucket{}
	}
	return out, nil
}

// bucketQuery roda uma agregação (key, total, count) e devolve os buckets.
func (s *Store) bucketQuery(ctx context.Context, sql string, args ...any) ([]Bucket, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Key, &b.Total, &b.Count); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// bucketSeries é como bucketQuery mas indexa por data (chave = 1ª coluna) para o merge do timeseries.
func (s *Store) bucketSeries(ctx context.Context, sql string, args ...any) (map[string]Bucket, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Bucket{}
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Key, &b.Total, &b.Count); err != nil {
			return nil, err
		}
		out[b.Key] = b
	}
	return out, rows.Err()
}

// ── Payment Links ─────────────────────────────────────────────────────────────

// CreatePaymentLink insere um novo link de pagamento e preenche ID, CreatedAt e Status.
func (s *Store) CreatePaymentLink(ctx context.Context, l *PaymentLink) error {
	// Converter []int para []int64 para o driver pgx
	pids := make([]int64, len(l.ProductIDs))
	for i, v := range l.ProductIDs {
		pids[i] = int64(v)
	}
	methods := l.Methods
	if len(methods) == 0 {
		methods = []string{"pix"}
	}
	coupons := l.Coupons
	if coupons == nil {
		coupons = []string{}
	}
	return s.db.QueryRow(ctx, `
		INSERT INTO pay_payment_links
		  (public_token, amount_cents, product_ids, methods, coupons, finish_url, return_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, status, created_at`,
		l.PublicToken, l.AmountCents, pids, methods, coupons, l.FinishURL, l.ReturnURL,
	).Scan(&l.ID, &l.Status, &l.CreatedAt)
}

// GetPaymentLinkByToken resolve um link pelo token público.
func (s *Store) GetPaymentLinkByToken(ctx context.Context, token string) (*PaymentLink, error) {
	var l PaymentLink
	var pids []int64
	err := s.db.QueryRow(ctx, `
		SELECT id, public_token, amount_cents, product_ids, methods, coupons,
		       finish_url, return_url, status, created_at
		FROM pay_payment_links WHERE public_token = $1`, token).
		Scan(&l.ID, &l.PublicToken, &l.AmountCents, &pids, &l.Methods, &l.Coupons,
			&l.FinishURL, &l.ReturnURL, &l.Status, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	l.ProductIDs = make([]int, len(pids))
	for i, v := range pids {
		l.ProductIDs[i] = int(v)
	}
	if l.Methods == nil {
		l.Methods = []string{}
	}
	if l.Coupons == nil {
		l.Coupons = []string{}
	}
	return &l, nil
}

// GetPaymentLink resolve um link pelo ID interno (admin).
func (s *Store) GetPaymentLink(ctx context.Context, id int64) (*PaymentLink, error) {
	var l PaymentLink
	var pids []int64
	err := s.db.QueryRow(ctx, `
		SELECT id, public_token, amount_cents, product_ids, methods, coupons,
		       finish_url, return_url, status, created_at
		FROM pay_payment_links WHERE id = $1`, id).
		Scan(&l.ID, &l.PublicToken, &l.AmountCents, &pids, &l.Methods, &l.Coupons,
			&l.FinishURL, &l.ReturnURL, &l.Status, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	l.ProductIDs = make([]int, len(pids))
	for i, v := range pids {
		l.ProductIDs[i] = int(v)
	}
	if l.Methods == nil {
		l.Methods = []string{}
	}
	if l.Coupons == nil {
		l.Coupons = []string{}
	}
	return &l, nil
}

// ListPaymentLinks lista todos os links de pagamento, mais recentes primeiro.
func (s *Store) ListPaymentLinks(ctx context.Context) ([]PaymentLink, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, public_token, amount_cents, product_ids, methods, coupons,
		       finish_url, return_url, status, created_at
		FROM pay_payment_links
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentLink
	for rows.Next() {
		var l PaymentLink
		var pids []int64
		if err := rows.Scan(&l.ID, &l.PublicToken, &l.AmountCents, &pids, &l.Methods, &l.Coupons,
			&l.FinishURL, &l.ReturnURL, &l.Status, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.ProductIDs = make([]int, len(pids))
		for i, v := range pids {
			l.ProductIDs[i] = int(v)
		}
		if l.Methods == nil {
			l.Methods = []string{}
		}
		if l.Coupons == nil {
			l.Coupons = []string{}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetPaymentLinkStatus altera o status de um link (active | inactive).
func (s *Store) SetPaymentLinkStatus(ctx context.Context, id int64, status string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE pay_payment_links SET status=$1 WHERE id=$2`, status, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ── Withdrawals (Saques) ──────────────────────────────────────────────────

// CreateWithdrawal insere um novo registro de saque e preenche ID e CreatedAt.
// O campo Destination NÃO é gravado no banco — nunca persistir dados de conta bancária
// no log nem na tabela (só o saque já executado fica registrado, sem o destino).
func (s *Store) CreateWithdrawal(ctx context.Context, w *Withdrawal) error {
	key := nullStrPtr(w.IdempotencyKey)
	return s.db.QueryRow(ctx, `
		INSERT INTO pay_withdrawals (amount_cents, status, public_token, idempotency_key, efi_id_envio, e2e_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`,
		w.AmountCents, w.Status, w.PublicToken, key,
		nullStrPtr(w.EFIIdEnvio), nullStrPtr(w.E2EID),
	).Scan(&w.ID, &w.CreatedAt)
}

// SetWithdrawalStatus atualiza o status de um saque casando por efi_id_envio OU e2e_id
// (o webhook de Pix Envio pode trazer qualquer um dos dois). Usado para aplicar o
// resultado final do payout (REALIZADO→completed / NAO_REALIZADO→failed). idEnvio e/ou
// e2eId podem vir vazios — strings vazias não casam (efi_id_envio/e2e_id são NULL ou
// preenchidos). Idempotente: só transiciona saques ainda em "processing".
func (s *Store) SetWithdrawalStatus(ctx context.Context, idEnvio, e2eID, status string) error {
	if idEnvio == "" && e2eID == "" {
		return nil
	}
	_, err := s.db.Exec(ctx, `
		UPDATE pay_withdrawals
		SET status = $1
		WHERE status = 'processing'
		  AND ( (efi_id_envio IS NOT NULL AND efi_id_envio = $2)
		     OR (e2e_id       IS NOT NULL AND e2e_id       = $3) )`,
		status, idEnvio, e2eID,
	)
	return err
}

// GetWithdrawalByIdempotencyKey devolve o withdrawal existente com aquela chave,
// ou nil (sem erro) se não existir.
func (s *Store) GetWithdrawalByIdempotencyKey(ctx context.Context, key string) (*Withdrawal, error) {
	var w Withdrawal
	var ikey, idEnvio, e2e *string
	err := s.db.QueryRow(ctx, `
		SELECT id, amount_cents, status, public_token, idempotency_key, efi_id_envio, e2e_id, created_at
		FROM pay_withdrawals WHERE idempotency_key = $1`, key).
		Scan(&w.ID, &w.AmountCents, &w.Status, &w.PublicToken, &ikey, &idEnvio, &e2e, &w.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if ikey != nil {
		w.IdempotencyKey = *ikey
	}
	if idEnvio != nil {
		w.EFIIdEnvio = *idEnvio
	}
	if e2e != nil {
		w.E2EID = *e2e
	}
	return &w, nil
}

// ListWithdrawals lista todos os saques, mais recentes primeiro.
func (s *Store) ListWithdrawals(ctx context.Context) ([]Withdrawal, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, amount_cents, status, public_token, efi_id_envio, e2e_id, created_at
		FROM pay_withdrawals
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Withdrawal
	for rows.Next() {
		var w Withdrawal
		var idEnvio, e2e *string
		if err := rows.Scan(&w.ID, &w.AmountCents, &w.Status, &w.PublicToken, &idEnvio, &e2e, &w.CreatedAt); err != nil {
			return nil, err
		}
		if idEnvio != nil {
			w.EFIIdEnvio = *idEnvio
		}
		if e2e != nil {
			w.E2EID = *e2e
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ── Statement (extrato de movimentos) ────────────────────────────────────────

// ListMovements devolve os movimentos financeiros do período [from, to], ordenados
// por data decrescente. Inclui:
//   - Cobranças pagas (direction="in", type="${method}_in"): usando paid_at como data.
//   - Saques registrados (direction="out", type="payout"): usando created_at como data.
//
// Não persiste SQL em handlers: toda a agregação fica aqui no store.
func (s *Store) ListMovements(ctx context.Context, from, to time.Time) ([]Movement, error) {
	// 1. Cobranças pagas no intervalo.
	chargeRows, err := s.db.Query(ctx, `
		SELECT
			c.id,
			COALESCE(c.method, 'pix') AS method,
			c.amount_cents,
			COALESCE(NULLIF(st.name,''),  NULLIF(cu.name,''),  '') AS customer_name,
			COALESCE(NULLIF(st.email,''), NULLIF(cu.email,''), '') AS customer_email,
			c.paid_at
		FROM pay_charges c
		LEFT JOIN pay_students  st ON st.id = c.student_id
		LEFT JOIN pay_customers cu ON cu.id = c.customer_id
		WHERE c.status = 'paid'
		  AND c.paid_at >= $1
		  AND c.paid_at < $2
		ORDER BY c.paid_at DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer chargeRows.Close()

	var out []Movement
	for chargeRows.Next() {
		var (
			id            int64
			method        string
			valueCents    int64
			customerName  string
			customerEmail string
			paidAt        time.Time
		)
		if err := chargeRows.Scan(&id, &method, &valueCents, &customerName, &customerEmail, &paidAt); err != nil {
			return nil, err
		}
		movType := method + "_in"
		if method == "" {
			movType = "pix_in"
		}
		out = append(out, Movement{
			ID:            id,
			Type:          movType,
			Direction:     "in",
			Method:        method,
			ValueCents:    valueCents,
			CustomerName:  customerName,
			CustomerEmail: customerEmail,
			Date:          paidAt,
		})
	}
	if err := chargeRows.Err(); err != nil {
		return nil, err
	}

	// 2. Saques registrados no intervalo.
	payoutRows, err := s.db.Query(ctx, `
		SELECT id, amount_cents, created_at
		FROM pay_withdrawals
		WHERE created_at >= $1
		  AND created_at < $2
		ORDER BY created_at DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer payoutRows.Close()

	for payoutRows.Next() {
		var (
			id         int64
			valueCents int64
			createdAt  time.Time
		)
		if err := payoutRows.Scan(&id, &valueCents, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, Movement{
			ID:         id,
			Type:       "payout",
			Direction:  "out",
			Method:     "",
			ValueCents: valueCents,
			Date:       createdAt,
		})
	}
	if err := payoutRows.Err(); err != nil {
		return nil, err
	}

	// Ordena todos os movimentos por data decrescente (combina cobranças + saques).
	sortMovements(out)
	return out, nil
}

// sortMovements ordena os movimentos por data decrescente sem dependência externa.
func sortMovements(mvs []Movement) {
	// Insertion sort simples — o número de movimentos por janela é pequeno.
	for i := 1; i < len(mvs); i++ {
		for j := i; j > 0 && mvs[j].Date.After(mvs[j-1].Date); j-- {
			mvs[j], mvs[j-1] = mvs[j-1], mvs[j]
		}
	}
}

// ── Coupons ───────────────────────────────────────────────────────────────────

// CreateCoupon insere um novo cupom. Retorna erro com código único se já existir
// outro cupom com o mesmo código (case-insensitive via UNIQUE em LOWER(code)).
func (s *Store) CreateCoupon(ctx context.Context, c Coupon) (Coupon, error) {
	err := s.db.QueryRow(ctx, `
		INSERT INTO pay_coupons (code, discount_type, discount_value, max_uses)
		VALUES ($1, $2, $3, $4)
		RETURNING id, code, discount_type, discount_value, max_uses, used_count, active, created_at`,
		c.Code, c.DiscountType, c.DiscountValue, c.MaxUses,
	).Scan(&c.ID, &c.Code, &c.DiscountType, &c.DiscountValue, &c.MaxUses, &c.UsedCount, &c.Active, &c.CreatedAt)
	return c, err
}

// ListCoupons lista todos os cupons, mais recentes primeiro.
func (s *Store) ListCoupons(ctx context.Context) ([]Coupon, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, code, discount_type, discount_value, max_uses, used_count, active, created_at
		FROM pay_coupons
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Coupon
	for rows.Next() {
		var c Coupon
		if err := rows.Scan(&c.ID, &c.Code, &c.DiscountType, &c.DiscountValue,
			&c.MaxUses, &c.UsedCount, &c.Active, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCouponByCode resolve um cupom pelo código (case-insensitive).
func (s *Store) GetCouponByCode(ctx context.Context, code string) (Coupon, error) {
	var c Coupon
	err := s.db.QueryRow(ctx, `
		SELECT id, code, discount_type, discount_value, max_uses, used_count, active, created_at
		FROM pay_coupons WHERE LOWER(code) = LOWER($1)`, code).
		Scan(&c.ID, &c.Code, &c.DiscountType, &c.DiscountValue,
			&c.MaxUses, &c.UsedCount, &c.Active, &c.CreatedAt)
	return c, err
}

// SetCouponActive ativa ou desativa um cupom pelo ID.
func (s *Store) SetCouponActive(ctx context.Context, id int64, active bool) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE pay_coupons SET active=$1 WHERE id=$2`, active, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// IncrementCouponUse incrementa used_count do cupom pelo ID.
func (s *Store) IncrementCouponUse(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx,
		`UPDATE pay_coupons SET used_count = used_count + 1 WHERE id=$1`, id)
	return err
}

// InsertChargeWithLink insere uma cobrança vinculada a um link de pagamento.
// Reutiliza o mesmo INSERT de InsertCharge mas adiciona link_id.
func (s *Store) InsertChargeWithLink(ctx context.Context, c *Charge, linkID int64) error {
	return s.db.QueryRow(ctx, `
		INSERT INTO pay_charges
		  (kind, subscription_id, student_id, customer_id, amount_cents, due_date, reference_month,
		   provider, provider_charge_id, correlation_id, public_token, payer_tax_id, method,
		   br_code, qr_code, pdf_url, barcode, link_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING id, status, created_at`,
		c.Kind, c.SubscriptionID, c.StudentID, c.CustomerID,
		c.AmountCents, dateToPgtype(c.DueDate), c.ReferenceMonth,
		c.Provider, nullStrPtr(c.ProviderChargeID), c.CorrelationID,
		nullStrPtr(c.PublicToken), nullStrPtr(c.payerTaxID), methodOrPix(c.Method),
		nullStrPtr(c.BRCode), nullStrPtr(c.QRCode),
		nullStrPtr(c.PDFURL), nullStrPtr(c.Barcode), linkID,
	).Scan(&c.ID, &c.Status, &c.CreatedAt)
}
