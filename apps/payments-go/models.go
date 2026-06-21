package main

import "time"

type Student struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	TaxID     string    `json:"taxId"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone"`
	CreatedAt time.Time `json:"createdAt"`
}

type Plan struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	AmountCents int64  `json:"amountCents"`
	DueDay      int    `json:"dueDay"`
	Active      bool   `json:"active"`
}

type Subscription struct {
	ID          int64  `json:"id"`
	StudentID   int64  `json:"studentId"`
	PlanID      int64  `json:"planId"`
	AmountCents int64  `json:"amountCents"`
	DueDay      int    `json:"dueDay"`
	Status      string `json:"status"`
}

type Charge struct {
	ID               int64            `json:"id"`
	Kind             string           `json:"kind"`
	SubscriptionID   *int64           `json:"subscriptionId,omitempty"`
	RecurrenceID     *int64           `json:"recurrenceId,omitempty"`
	StudentID        *int64           `json:"studentId,omitempty"`
	CustomerID       *int64           `json:"customerId,omitempty"`
	AmountCents      int64            `json:"amountCents"`
	DueDate          string           `json:"dueDate"`                  // YYYY-MM-DD
	ReferenceMonth   *string          `json:"referenceMonth,omitempty"` // YYYY-MM
	Status           string           `json:"status"`
	Provider         string           `json:"provider"`
	ProviderChargeID string           `json:"providerChargeId"`
	CorrelationID    string           `json:"correlationId"`
	PublicToken      string           `json:"publicToken,omitempty"`
	Method           string           `json:"method"` // pix | boleto
	BRCode           string           `json:"brCode"` // pix: copia-e-cola · boleto: linha digitável
	QRCode           string           `json:"qrCode"`
	PDFURL           string           `json:"pdfUrl,omitempty"`  // boleto: link do PDF
	Barcode          string           `json:"barcode,omitempty"` // boleto: código de barras
	PaidAt           *time.Time       `json:"paidAt,omitempty"`
	CreatedAt        time.Time        `json:"createdAt"`
	PayerName        string           `json:"payerName,omitempty"`
	PayerEmail       string           `json:"payerEmail,omitempty"` // só preenchido na listagem (busca)
	Refusal          *CardRefusalInfo `json:"refusal,omitempty"`    // cartão: motivo de recusa (status unpaid)
	LinkID           *int64           `json:"linkId,omitempty"`     // link de pagamento de origem (opcional)

	payerTaxID string // snapshot p/ insert; não serializa
}

// Recurrence é um contrato de PIX Automático (recorrência BACEN). Na fase 1 só usa
// subscription_id (mensalidade, jornada 2); product_id/customer_id ficam prontos p/ a fase 2.
type Recurrence struct {
	ID             int64     `json:"id"`
	SubscriptionID *int64    `json:"subscriptionId,omitempty"`
	ProductID      *int64    `json:"productId,omitempty"`
	CustomerID     *int64    `json:"customerId,omitempty"`
	PayerTaxID     string    `json:"payerTaxId"`
	PayerName      string    `json:"payerName"`
	AmountCents    int64     `json:"amountCents"`
	Periodicity    string    `json:"periodicity"`
	DueDay         *int      `json:"dueDay,omitempty"`
	StartDate      string    `json:"startDate"`         // YYYY-MM-DD
	EndDate        string    `json:"endDate,omitempty"` // YYYY-MM-DD
	Journey        int       `json:"journey"`
	EfiIDRec       string    `json:"efiIdRec,omitempty"`
	BRCode         string    `json:"brCode"`
	QRCode         string    `json:"qrCode"`
	Status         string    `json:"status"`
	PublicToken    string    `json:"publicToken,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type StatsPeriod struct {
	PaidTotal     int64 `json:"paidTotal"`
	PaidCount     int64 `json:"paidCount"`
	PendingTotal  int64 `json:"pendingTotal"`
	PendingCount  int64 `json:"pendingCount"`
	ExpiredCount  int64 `json:"expiredCount"`
	CanceledCount int64 `json:"canceledCount"`
}

type StatsResult struct {
	Month   StatsPeriod `json:"month"`
	AllTime StatsPeriod `json:"allTime"`
}

type Product struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceCents  int64  `json:"priceCents"`
	Active      bool   `json:"active"`
	// Produto recorrente (assinatura, fase 2). Quando Recurring=true, Periodicity
	// (MENSAL/etc) e DueDay (1–28) descrevem os ciclos do PIX Automático.
	Recurring   bool   `json:"recurring"`
	Periodicity string `json:"periodicity,omitempty"`
	DueDay      *int   `json:"dueDay,omitempty"`
	// ChargeOnSubscribe: true = cobra a 1ª parcela logo após autorizar (auto-débito);
	// false = a 1ª parcela cai no due_day.
	ChargeOnSubscribe bool `json:"chargeOnSubscribe"`
}

type Customer struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"userId"`
	TaxID  string `json:"taxId"`
	Phone  string `json:"phone"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

// CustomerWithStats é uma linha da lista de clientes (admin) com agregados das compras.
type CustomerWithStats struct {
	ID             int64      `json:"id"`
	UserID         int64      `json:"userId"`
	TaxID          string     `json:"taxId"`
	Phone          string     `json:"phone"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	CreatedAt      time.Time  `json:"createdAt"`
	ChargesCount   int64      `json:"chargesCount"`
	PaidCount      int64      `json:"paidCount"`
	PaidTotalCents int64      `json:"paidTotalCents"`
	LastChargeAt   *time.Time `json:"lastChargeAt,omitempty"`
}

// CustomerCharge é uma compra do cliente (cobrança + itens) no detalhe.
type CustomerCharge struct {
	ID            int64        `json:"id"`
	Kind          string       `json:"kind"`
	AmountCents   int64        `json:"amountCents"`
	Status        string       `json:"status"`
	DueDate       string       `json:"dueDate"`
	CorrelationID string       `json:"correlationId"`
	PaidAt        *time.Time   `json:"paidAt,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
	Items         []ChargeItem `json:"items"`
}

// CustomerDetail é o cliente + histórico de compras + assinaturas (GET /customers/{id}).
type CustomerDetail struct {
	ID          int64            `json:"id"`
	UserID      int64            `json:"userId"`
	TaxID       string           `json:"taxId"`
	Phone       string           `json:"phone"`
	Name        string           `json:"name"`
	Email       string           `json:"email"`
	CreatedAt   time.Time        `json:"createdAt"`
	Charges     []CustomerCharge `json:"charges"`
	Recurrences []Recurrence     `json:"recurrences"`
}

type CartItem struct {
	ProductID int64 `json:"productId"`
	Quantity  int   `json:"quantity"`
}

type ChargeItem struct {
	ProductID  *int64 `json:"productId,omitempty"`
	Name       string `json:"name"`
	PriceCents int64  `json:"priceCents"`
	Quantity   int    `json:"quantity"`
}

// Withdrawal representa um saque (payout) do saldo Efí para a conta bancária.
// A transferência real só acontece quando PAYOUT_ENABLED=true; caso contrário,
// o registro fica em status "disabled" (flag desligada — a operação não foi executada).
type Withdrawal struct {
	ID             int64     `json:"id"`
	AmountCents    int64     `json:"amountCents"`
	Status         string    `json:"status"` // "processing" | "completed" | "failed" | "disabled"
	PublicToken    string    `json:"publicToken"`
	IdempotencyKey string    `json:"idempotencyKey,omitempty"`
	Destination    string    `json:"-"` // nunca serializa — não expor no JSON
	CreatedAt      time.Time `json:"createdAt"`
}

// Movement representa um movimento financeiro no extrato: entrada (pagamento recebido)
// ou saída (taxa, saque). Derivado de cobranças pagas, taxas estimadas e saques.
type Movement struct {
	ID            int64     `json:"id"`
	Type          string    `json:"type"`       // "pix_in" | "boleto_in" | "card_in" | "fee" | "payout"
	Direction     string    `json:"direction"`  // "in" | "out"
	Method        string    `json:"method"`     // "pix" | "boleto" | "card" | ""
	ValueCents    int64     `json:"valueCents"` // sempre positivo
	CustomerName  string    `json:"customerName,omitempty"`
	CustomerEmail string    `json:"customerEmail,omitempty"`
	Date          time.Time `json:"date"`
}

// Coupon é um cupom de desconto aplicável no checkout. discount_type define o tipo:
// "fixed" (desconto em centavos) ou "percent" (1–100). max_uses=-1 = ilimitado.
type Coupon struct {
	ID            int64     `json:"id"`
	Code          string    `json:"code"`
	DiscountType  string    `json:"discountType"`  // "fixed" | "percent"
	DiscountValue int64     `json:"discountValue"` // centavos (fixed) ou 1–100 (percent)
	MaxUses       int64     `json:"maxUses"`       // -1 = ilimitado
	UsedCount     int64     `json:"usedCount"`
	Active        bool      `json:"active"`
	CreatedAt     time.Time `json:"createdAt"`
}

// PaymentLink é um link de pagamento reutilizável. O pagador acessa por /link/{token},
// escolhe o método e cria uma charge vinculada (link_id). AmountCents nil = valor livre.
type PaymentLink struct {
	ID          int64     `json:"id"`
	PublicToken string    `json:"publicToken"`
	AmountCents *int64    `json:"amountCents,omitempty"` // nil = valor a definir pelo pagador
	ProductIDs  []int     `json:"productIds"`
	Methods     []string  `json:"methods"` // "pix" | "card" | "boleto"
	Coupons     []string  `json:"coupons"`
	FinishURL   string    `json:"finishUrl,omitempty"`
	ReturnURL   string    `json:"returnUrl,omitempty"`
	Status      string    `json:"status"` // "active" | "inactive"
	CreatedAt   time.Time `json:"createdAt"`
}
