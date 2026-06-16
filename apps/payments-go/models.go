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
	StudentID        int64      `json:"studentId"`
	AmountCents      int64      `json:"amountCents"`
	DueDate          string     `json:"dueDate"`                  // YYYY-MM-DD
	ReferenceMonth   *string    `json:"referenceMonth,omitempty"` // YYYY-MM
	Status           string     `json:"status"`
	Provider         string     `json:"provider"`
	ProviderChargeID string     `json:"providerChargeId"`
	CorrelationID    string     `json:"correlationId"`
	BRCode           string     `json:"brCode"`
	QRCode           string     `json:"qrCode"`
	PaidAt           *time.Time `json:"paidAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
}
