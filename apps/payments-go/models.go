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
	ID               int64      `json:"id"`
	Kind             string     `json:"kind"`
	SubscriptionID   *int64     `json:"subscriptionId,omitempty"`
	StudentID        *int64     `json:"studentId,omitempty"`
	CustomerID       *int64     `json:"customerId,omitempty"`
	AmountCents      int64      `json:"amountCents"`
	DueDate          string     `json:"dueDate"`                  // YYYY-MM-DD
	ReferenceMonth   *string    `json:"referenceMonth,omitempty"` // YYYY-MM
	Status           string     `json:"status"`
	Provider         string     `json:"provider"`
	ProviderChargeID string     `json:"providerChargeId"`
	CorrelationID    string     `json:"correlationId"`
	PublicToken      string     `json:"publicToken,omitempty"`
	BRCode           string     `json:"brCode"`
	QRCode           string     `json:"qrCode"`
	PaidAt           *time.Time `json:"paidAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`

	payerTaxID string // snapshot p/ insert; não serializa
}

type Product struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceCents  int64  `json:"priceCents"`
	Active      bool   `json:"active"`
}

type Customer struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"userId"`
	TaxID  string `json:"taxId"`
	Phone  string `json:"phone"`
	Name   string `json:"name"`
	Email  string `json:"email"`
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
