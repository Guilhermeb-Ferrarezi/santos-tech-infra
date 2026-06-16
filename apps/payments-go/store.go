package main

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ db *pgxpool.Pool }

// ── Students ──────────────────────────────────────────────────────────────

func (s *Store) CreateStudent(ctx context.Context, st *Student) error {
	return s.db.QueryRow(ctx,
		`INSERT INTO pay_students (name, tax_id, email, phone) VALUES ($1,$2,$3,$4) RETURNING id, created_at`,
		st.Name, st.TaxID, st.Email, st.Phone).Scan(&st.ID, &st.CreatedAt)
}

func (s *Store) ListStudents(ctx context.Context) ([]Student, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, tax_id, email, phone, created_at FROM pay_students ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Student{}
	for rows.Next() {
		var st Student
		if err := rows.Scan(&st.ID, &st.Name, &st.TaxID, &st.Email, &st.Phone, &st.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) GetStudent(ctx context.Context, id int64) (*Student, error) {
	var st Student
	err := s.db.QueryRow(ctx,
		`SELECT id, name, tax_id, email, phone, created_at FROM pay_students WHERE id=$1`, id).
		Scan(&st.ID, &st.Name, &st.TaxID, &st.Email, &st.Phone, &st.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// ── Plans ─────────────────────────────────────────────────────────────────

func (s *Store) CreatePlan(ctx context.Context, p *Plan) error {
	return s.db.QueryRow(ctx,
		`INSERT INTO pay_plans (name, amount_cents, due_day) VALUES ($1,$2,$3) RETURNING id, active`,
		p.Name, p.AmountCents, p.DueDay).Scan(&p.ID, &p.Active)
}

func (s *Store) ListPlans(ctx context.Context) ([]Plan, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, amount_cents, due_day, active FROM pay_plans ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Name, &p.AmountCents, &p.DueDay, &p.Active); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── Subscriptions ─────────────────────────────────────────────────────────

func (s *Store) CreateSubscription(ctx context.Context, sub *Subscription) error {
	return s.db.QueryRow(ctx,
		`INSERT INTO pay_subscriptions (student_id, plan_id, amount_cents, due_day) VALUES ($1,$2,$3,$4) RETURNING id, status`,
		sub.StudentID, sub.PlanID, sub.AmountCents, sub.DueDay).Scan(&sub.ID, &sub.Status)
}

func (s *Store) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := s.db.Query(ctx, `SELECT id, student_id, plan_id, amount_cents, due_day, status FROM pay_subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.StudentID, &sub.PlanID, &sub.AmountCents, &sub.DueDay, &sub.Status); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *Store) SetSubscriptionStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.Exec(ctx, `UPDATE pay_subscriptions SET status=$2 WHERE id=$1`, id, status)
	return err
}

// SubDue agrupa uma assinatura a cobrar com o aluno correspondente (usado pela recorrência).
type SubDue struct {
	Sub     Subscription
	Student Student
}

// SubscriptionsDueToday: ativas cujo due_day == dia atual e sem charge no reference_month.
func (s *Store) SubscriptionsDueToday(ctx context.Context, day int, refMonth string) ([]SubDue, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sub.id, sub.student_id, sub.plan_id, sub.amount_cents, sub.due_day, sub.status,
		       st.id, st.name, st.tax_id, st.email, st.phone, st.created_at
		FROM pay_subscriptions sub
		JOIN pay_students st ON st.id = sub.student_id
		WHERE sub.status='active' AND sub.due_day=$1
		  AND NOT EXISTS (
		    SELECT 1 FROM pay_charges c
		    WHERE c.subscription_id = sub.id AND c.reference_month = $2
		  )`, day, refMonth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SubDue{}
	for rows.Next() {
		var row SubDue
		if err := rows.Scan(&row.Sub.ID, &row.Sub.StudentID, &row.Sub.PlanID, &row.Sub.AmountCents,
			&row.Sub.DueDay, &row.Sub.Status, &row.Student.ID, &row.Student.Name, &row.Student.TaxID,
			&row.Student.Email, &row.Student.Phone, &row.Student.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ── Charges ───────────────────────────────────────────────────────────────

func (s *Store) InsertCharge(ctx context.Context, c *Charge) error {
	return s.db.QueryRow(ctx, `
		INSERT INTO pay_charges
		  (kind, subscription_id, student_id, customer_id, amount_cents, due_date, reference_month,
		   provider, provider_charge_id, correlation_id, public_token, payer_tax_id, br_code, qr_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id, status, created_at`,
		c.Kind, c.SubscriptionID, c.StudentID, c.CustomerID, c.AmountCents, c.DueDate, c.ReferenceMonth,
		c.Provider, c.ProviderChargeID, c.CorrelationID, nullStr(c.PublicToken), nullStr(c.payerTaxID), c.BRCode, c.QRCode).
		Scan(&c.ID, &c.Status, &c.CreatedAt)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) ListCharges(ctx context.Context, status string, studentID int64) ([]Charge, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, kind, subscription_id, student_id, amount_cents, due_date::text, reference_month,
		       status, provider, COALESCE(provider_charge_id,''), correlation_id,
		       COALESCE(br_code,''), COALESCE(qr_code,''), paid_at, created_at
		FROM pay_charges
		WHERE ($1='' OR status=$1) AND ($2=0 OR student_id=$2)
		ORDER BY created_at DESC`, status, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Charge{}
	for rows.Next() {
		var c Charge
		if err := rows.Scan(&c.ID, &c.Kind, &c.SubscriptionID, &c.StudentID, &c.AmountCents, &c.DueDate,
			&c.ReferenceMonth, &c.Status, &c.Provider, &c.ProviderChargeID, &c.CorrelationID,
			&c.BRCode, &c.QRCode, &c.PaidAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCharge(ctx context.Context, id int64) (*Charge, error) {
	var c Charge
	err := s.db.QueryRow(ctx, `
		SELECT id, kind, subscription_id, student_id, amount_cents, due_date::text, reference_month,
		       status, provider, COALESCE(provider_charge_id,''), correlation_id,
		       COALESCE(br_code,''), COALESCE(qr_code,''), paid_at, created_at
		FROM pay_charges WHERE id=$1`, id).
		Scan(&c.ID, &c.Kind, &c.SubscriptionID, &c.StudentID, &c.AmountCents, &c.DueDate,
			&c.ReferenceMonth, &c.Status, &c.Provider, &c.ProviderChargeID, &c.CorrelationID,
			&c.BRCode, &c.QRCode, &c.PaidAt, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) MarkChargePaid(ctx context.Context, correlationID string) error {
	_, err := s.db.Exec(ctx, `UPDATE pay_charges SET status='paid', paid_at=now() WHERE correlation_id=$1 AND status='pending'`, correlationID)
	return err
}

func (s *Store) MarkChargeExpired(ctx context.Context, correlationID string) error {
	_, err := s.db.Exec(ctx, `UPDATE pay_charges SET status='expired' WHERE correlation_id=$1 AND status='pending'`, correlationID)
	return err
}

// ── Webhook idempotência ──────────────────────────────────────────────────

// MarkWebhookSeen retorna true se é a 1ª vez que vemos este evento (deve processar).
func (s *Store) MarkWebhookSeen(ctx context.Context, id, typ string, payload []byte) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`INSERT INTO pay_webhook_events (id, type, payload) VALUES ($1,$2,$3) ON CONFLICT (id) DO NOTHING`,
		id, typ, payload)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) PublicTokenByCorrelation(ctx context.Context, correlationID string) (string, error) {
	var tok *string
	err := s.db.QueryRow(ctx, `SELECT public_token FROM pay_charges WHERE correlation_id=$1`, correlationID).Scan(&tok)
	if err != nil || tok == nil {
		return "", err
	}
	return *tok, nil
}

// ── Products ──────────────────────────────────────────────────────────────

func (s *Store) CreateProduct(ctx context.Context, p *Product) error {
	return s.db.QueryRow(ctx,
		`INSERT INTO pay_products (slug, name, description, price_cents) VALUES ($1,$2,$3,$4) RETURNING id, active`,
		p.Slug, p.Name, p.Description, p.PriceCents).Scan(&p.ID, &p.Active)
}

func (s *Store) ListProducts(ctx context.Context) ([]Product, error) {
	rows, err := s.db.Query(ctx, `SELECT id, slug, name, description, price_cents, active FROM pay_products ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.PriceCents, &p.Active); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProductBySlug(ctx context.Context, slug string) (*Product, error) {
	var p Product
	err := s.db.QueryRow(ctx,
		`SELECT id, slug, name, description, price_cents, active FROM pay_products WHERE slug=$1 AND active=true`, slug).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.PriceCents, &p.Active)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProductByID retorna só produtos ATIVOS — usado no carrinho e no checkout, para
// não exibir nem cobrar item desativado depois de já ter entrado no carrinho.
func (s *Store) GetProductByID(ctx context.Context, id int64) (*Product, error) {
	var p Product
	err := s.db.QueryRow(ctx,
		`SELECT id, slug, name, description, price_cents, active FROM pay_products WHERE id=$1 AND active=true`, id).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.PriceCents, &p.Active)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) DeleteProduct(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM pay_products WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateProduct(ctx context.Context, p *Product) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE pay_products SET name=$2, description=$3, price_cents=$4, active=$5 WHERE id=$1`,
		p.ID, p.Name, p.Description, p.PriceCents, p.Active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ── Customers ─────────────────────────────────────────────────────────────

// UpsertCustomer garante o registro do cliente SEM sobrescrever dados já preenchidos.
// O DO UPDATE é um no-op (re-grava o próprio user_id) só para o RETURNING devolver a
// linha existente — tax_id/phone/name/email persistidos não são tocados.
func (s *Store) UpsertCustomer(ctx context.Context, userID int64) (*Customer, error) {
	var c Customer
	err := s.db.QueryRow(ctx, `
		INSERT INTO pay_customers (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO UPDATE SET user_id=EXCLUDED.user_id
		RETURNING id, user_id, tax_id, phone, name, email`,
		userID).Scan(&c.ID, &c.UserID, &c.TaxID, &c.Phone, &c.Name, &c.Email)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) GetCustomerByUserID(ctx context.Context, userID int64) (*Customer, error) {
	var c Customer
	err := s.db.QueryRow(ctx,
		`SELECT id, user_id, tax_id, phone, name, email FROM pay_customers WHERE user_id=$1`, userID).
		Scan(&c.ID, &c.UserID, &c.TaxID, &c.Phone, &c.Name, &c.Email)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) UpdateCustomerData(ctx context.Context, userID int64, taxID, phone string) error {
	_, err := s.db.Exec(ctx, `UPDATE pay_customers SET tax_id=$2, phone=$3 WHERE user_id=$1`, userID, taxID, phone)
	return err
}

// ── Charge items + charges por token/cliente ──────────────────────────────

func (s *Store) InsertChargeItems(ctx context.Context, chargeID int64, items []ChargeItem) error {
	for _, it := range items {
		if _, err := s.db.Exec(ctx,
			`INSERT INTO pay_charge_items (charge_id, product_id, name, price_cents, quantity) VALUES ($1,$2,$3,$4,$5)`,
			chargeID, it.ProductID, it.Name, it.PriceCents, it.Quantity); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetChargeByPublicToken(ctx context.Context, token string) (*Charge, error) {
	var c Charge
	err := s.db.QueryRow(ctx, `
		SELECT id, kind, student_id, customer_id, amount_cents, due_date::text, status,
		       COALESCE(br_code,''), COALESCE(qr_code,''), correlation_id, paid_at, created_at
		FROM pay_charges WHERE public_token=$1`, token).
		Scan(&c.ID, &c.Kind, &c.StudentID, &c.CustomerID, &c.AmountCents, &c.DueDate, &c.Status,
			&c.BRCode, &c.QRCode, &c.CorrelationID, &c.PaidAt, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ListChargesByCustomer(ctx context.Context, customerID int64) ([]Charge, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, kind, amount_cents, due_date::text, status, COALESCE(br_code,''), correlation_id, paid_at, created_at
		FROM pay_charges WHERE customer_id=$1 ORDER BY created_at DESC`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Charge{}
	for rows.Next() {
		var c Charge
		if err := rows.Scan(&c.ID, &c.Kind, &c.AmountCents, &c.DueDate, &c.Status, &c.BRCode, &c.CorrelationID, &c.PaidAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
