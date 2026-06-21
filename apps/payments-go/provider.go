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

// RecurrenceRequest é a entrada para criar um contrato de PIX Automático na Efí
// (POST /v2/rec). Fase 1: jornada 2 (só autoriza, sem débito imediato), valor fixo.
type RecurrenceRequest struct {
	Contract    string // vinculo.contrato — identificador legível do contrato (ex: assinatura #ID)
	Object      string // vinculo.objeto — descrição do que está sendo cobrado
	PayerName   string
	PayerTaxID  string
	AmountCents int64
	Periodicity string // MENSAL|SEMANAL|TRIMESTRAL|SEMESTRAL|ANUAL (vocabulário do app)
	StartDate   string // YYYY-MM-DD
	EndDate     string // YYYY-MM-DD (opcional)
}

// RecurrenceResult é o retorno da criação/consulta de uma recorrência na Efí.
type RecurrenceResult struct {
	EfiIDRec string // idRec do contrato na Efí
	BRCode   string // copia-e-cola de autorização (jornada 2)
	QRCode   string // imagem do QR de autorização (base64/data-uri) ou URL
	Status   string // status mapeado p/ o app
}

// RecurringChargeRequest é a entrada para criar a cobrança de um ciclo (PUT /v2/cobr/{txid}),
// vinculada ao contrato via EfiIDRec.
type RecurringChargeRequest struct {
	CorrelationID string // txid da cobr (o nosso stpay...)
	EfiIDRec      string // idRec do contrato
	AmountCents   int64
	PayerName     string
	PayerTaxID    string
	DueDate       string // YYYY-MM-DD (calendario.dataDeVencimento)
}

// RecurrenceJornada3Request é a entrada para criar um contrato de PIX Automático na
// Jornada 3 (POST /v2/rec com a 1ª cobrança vinculada): autoriza a recorrência E paga
// a 1ª parcela (vencimento hoje) num ÚNICO QR/copia-e-cola. ChargeCorrelationID é o
// txid (determinístico/seguro) da 1ª cobr imediata, criada antes de vincular ao contrato.
type RecurrenceJornada3Request struct {
	Contract            string // vinculo.contrato
	Object              string // vinculo.objeto
	PayerName           string
	PayerTaxID          string
	AmountCents         int64
	Periodicity         string // vocabulário do app (MENSAL|…)
	StartDate           string // YYYY-MM-DD (calendario.dataInicial)
	EndDate             string // YYYY-MM-DD (opcional)
	ChargeCorrelationID string // txid da 1ª cobr (cob imediata) — vence hoje
	FirstDueDate        string // YYYY-MM-DD do vencimento da 1ª parcela (hoje)
	Description         string // solicitacaoPagador da 1ª cob
}

// RecEvent é um evento de mudança de status de uma recorrência, vindo do webhook rec.
type RecEvent struct {
	EfiIDRec string // idRec do contrato afetado
	Status   string // status mapeado p/ o app
	Raw      []byte
}

// BoletoRequest é a entrada para emitir um boleto na API Cobranças do Efí
// (POST /v1/charge/one-step). NotificationURL recebe os avisos de mudança de status.
type BoletoRequest struct {
	AmountCents     int64
	PayerName       string
	PayerTaxID      string // CPF (só dígitos)
	PayerEmail      string // opcional
	PayerPhone      string // opcional
	Description     string // vira o nome do item
	DueDate         string // YYYY-MM-DD (banking_billet.expire_at)
	CustomID        string // metadata.custom_id (nosso correlation_id)
	NotificationURL string // metadata.notification_url
}

// BoletoResult é o retorno da emissão do boleto.
type BoletoResult struct {
	ChargeID string // data.charge_id (numérico, guardado como string em provider_charge_id)
	Line     string // data.barcode — linha digitável (copia-e-cola do boleto)
	PDFURL   string // data.pdf.charge — link do PDF
	Status   string // data.status (waiting|paid|...)
}

// BoletoEvent é um item de GET /v1/notification/{token} (mudança de status de cobrança).
type BoletoEvent struct {
	ChargeID string // identifiers.charge_id
	Status   string // status.current (paid|waiting|unpaid|canceled|...)
}

// PaymentProvider isola o núcleo do gateway. Fase atual: efiProvider.
type PaymentProvider interface {
	CreateCharge(ctx context.Context, req ChargeRequest) (ChargeResult, error)
	GetCharge(ctx context.Context, providerChargeID string) (ChargeResult, error)
	ParseWebhook(headers map[string][]string, body []byte) ([]WebhookEvent, error)
}

// ── Cartão (API Cobranças) ────────────────────────────────────────────────

// Installment descreve uma opção de parcelamento devolvida pelo Efí.
type Installment struct {
	Number          int     `json:"number"`
	ValueCents      int64   `json:"valueCents"`
	InterestPercent float64 `json:"interestPercent"`
}

// CardBillingAddress é o endereço de cobrança do portador.
type CardBillingAddress struct {
	Street       string `json:"street"`
	Number       string `json:"number"`
	Neighborhood string `json:"neighborhood"`
	ZipCode      string `json:"zipCode"`
	City         string `json:"city"`
	State        string `json:"state"`
}

// CardCustomer são os dados do portador do cartão (não-sensíveis — nunca PAN/CVV).
type CardCustomer struct {
	Name      string `json:"name"`
	TaxID     string `json:"taxId"` // CPF (só dígitos)
	Email     string `json:"email"`
	Phone     string `json:"phone,omitempty"`
	BirthDate string `json:"birthDate,omitempty"` // YYYY-MM-DD
}

// CardChargeInput é o payload de criação de cobrança com cartão recebido do frontend.
// ATENÇÃO: nunca deve conter campos de PAN/CVV — o tokenizador Efí no frontend envia
// apenas o payment_token. O handler rejeita explicitamente qualquer corpo com esses campos.
type CardChargeInput struct {
	PaymentToken   string             `json:"paymentToken"`
	Installments   int                `json:"installments"`
	Customer       CardCustomer       `json:"customer"`
	BillingAddress CardBillingAddress `json:"billingAddress"`
	// Campos de negócio opcionais
	AmountCents int64  `json:"amountCents"`
	Description string `json:"description"`
	// Campos de contexto: a cobrança pode vir de um cliente existente
	CustomerID *int64 `json:"customerId,omitempty"`
}

// CardChargeResult é o retorno da criação de cobrança com cartão.
type CardChargeResult struct {
	ChargeID string `json:"chargeId"`
	Status   string `json:"status"` // status mapeado (waiting|paid|unpaid|canceled)
	Message  string `json:"message,omitempty"`
}

// CardRefusalInfo carrega detalhes de recusa do cartão (status unpaid).
type CardRefusalInfo struct {
	Reason string `json:"reason"`
	Retry  bool   `json:"retry"`
}
