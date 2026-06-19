package main

import "context"

// ProviderError carrega uma mensagem amigável vinda do gateway (ex: "Valor máximo
// permitido por cobrança: R$ 100,00"), para ser repassada ao cliente sem expor 502.
type ProviderError struct {
	Message string
	Status  int
}

func (e *ProviderError) Error() string { return e.Message }

type ChargeRequest struct {
	CorrelationID string
	AmountCents   int64
	PayerName     string
	PayerTaxID    string
	Description   string
	ExpiresAt     string // RFC3339
}

type ChargeResult struct {
	ProviderChargeID string
	CorrelationID    string // correlationID efetivo do gateway (o nosso txid stpay... na Efí)
	BRCode           string // Pix copia-e-cola
	QRCode           string // imagem (base64/data-uri) ou URL
	PaymentLink      string // link de checkout hospedado pelo gateway
	Status           string
}

type WebhookEvent struct {
	ID               string // id único do evento (idempotência)
	Type             string // CHARGE_PAID | CHARGE_EXPIRED | CHARGE_CREATED
	CorrelationID    string
	ProviderChargeID string
	Raw              []byte
}

// PaymentProvider isola o núcleo do gateway. Fase atual: efiProvider.
type PaymentProvider interface {
	CreateCharge(ctx context.Context, req ChargeRequest) (ChargeResult, error)
	GetCharge(ctx context.Context, providerChargeID string) (ChargeResult, error)
	ParseWebhook(headers map[string][]string, body []byte) ([]WebhookEvent, error)
}
