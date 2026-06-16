package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

type dotfyProvider struct {
	base      string
	key       string
	secret    string // webhook secret (HMAC); vazio = sem verificação (apenas dev)
	sigHeader string // header HTTP que carrega a assinatura
	client    *http.Client
}

func newDotfyProvider(cfg Config) *dotfyProvider {
	return &dotfyProvider{
		base:      cfg.DotfyBaseURL,
		key:       cfg.DotfyAPIKey,
		secret:    cfg.DotfyWebhookSecret,
		sigHeader: cfg.DotfyWebhookSigHdr,
		client:    &http.Client{Timeout: 20 * time.Second},
	}
}

// headerValue procura um header de forma case-insensitive (http.Header já canoniza,
// mas aceitamos mapas crus de teste também).
func headerValue(headers map[string][]string, name string) string {
	if v, ok := headers[textproto.CanonicalMIMEHeaderKey(name)]; ok && len(v) > 0 {
		return v[0]
	}
	for k, v := range headers {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// verifyWebhookSignature computa HMAC-SHA256(corpo cru, secret) em hex e compara em
// tempo constante com o header de assinatura. Aceita o valor com ou sem prefixo
// "sha256=". O encoding/nome exato do header devem ser confirmados na conta Dotfy
// (ajustáveis por DOTFY_WEBHOOK_SIG_HEADER); a defesa em si (HMAC + constant-time +
// fail-closed) independe desses detalhes.
func (p *dotfyProvider) verifyWebhookSignature(headers map[string][]string, body []byte) error {
	provided := headerValue(headers, p.sigHeader)
	if provided == "" {
		return errors.New("assinatura ausente no webhook")
	}
	provided = strings.TrimSpace(strings.TrimPrefix(provided, "sha256="))
	mac := hmac.New(sha256.New, []byte(p.secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(provided)) {
		return errors.New("assinatura do webhook inválida")
	}
	return nil
}

// dotfyChargeReq — formato real confirmado contra a API (app.dotfy.com.br).
// IMPORTANTE: `value` é em REAIS (não centavos). O `correlationID` enviado é
// ignorado pelo Dotfy, que gera o seu próprio — reconciliamos pelo valor retornado.
type dotfyChargeReq struct {
	CorrelationID string  `json:"correlationID,omitempty"`
	Value         float64 `json:"value"` // em REAIS
	PayerName     string  `json:"payerName,omitempty"`
	PayerTaxID    string  `json:"payerTaxId,omitempty"`
	Description   string  `json:"description,omitempty"`
	ExpiresAt     string  `json:"expiresAt,omitempty"`
}

// dotfyChargeResp — resposta envelopada {success, data}. No data: `qrCode` é o
// copia-e-cola, `qrCodeImage` a imagem, `id`/`chargeId` os identificadores, e
// `correlationID` o id efetivo gerado pelo Dotfy. `value` volta em centavos.
type dotfyChargeResp struct {
	Success bool `json:"success"`
	Data    struct {
		ID            string `json:"id"`
		ChargeID      string `json:"chargeId"`
		CorrelationID string `json:"correlationID"`
		QRCode        string `json:"qrCode"`      // copia-e-cola
		QRCodeImage   string `json:"qrCodeImage"` // imagem (data-uri base64)
		PaymentLink   string `json:"paymentLink"`
		ExpiresAt     string `json:"expiresAt"`
		Value         int64  `json:"value"` // centavos
	} `json:"data"`
}

func (r dotfyChargeResp) toResult() ChargeResult {
	return ChargeResult{
		ProviderChargeID: r.Data.ID,
		CorrelationID:    r.Data.CorrelationID,
		BRCode:           r.Data.QRCode,
		QRCode:           r.Data.QRCodeImage,
		PaymentLink:      r.Data.PaymentLink,
	}
}

func (p *dotfyProvider) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		if readErr != nil {
			return nil, fmt.Errorf("dotfy %s %s: status %d (falha ao ler resposta: %w)", method, path, res.StatusCode, readErr)
		}
		// o Dotfy retorna {"error":"mensagem amigável"} — repassa ao cliente
		var de struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &de) == nil && de.Error != "" {
			return nil, &ProviderError{Message: de.Error, Status: res.StatusCode}
		}
		return nil, fmt.Errorf("dotfy %s %s: status %d: %s", method, path, res.StatusCode, data)
	}
	return data, nil
}

func (p *dotfyProvider) CreateCharge(ctx context.Context, req ChargeRequest) (ChargeResult, error) {
	data, err := p.do(ctx, http.MethodPost, "/api/charges", dotfyChargeReq{
		CorrelationID: req.CorrelationID,
		Value:         float64(req.AmountCents) / 100, // API espera REAIS
		PayerName:     req.PayerName,
		PayerTaxID:    req.PayerTaxID,
		Description:   req.Description,
		ExpiresAt:     req.ExpiresAt,
	})
	if err != nil {
		return ChargeResult{}, err
	}
	var r dotfyChargeResp
	if err := json.Unmarshal(data, &r); err != nil {
		return ChargeResult{}, err
	}
	return r.toResult(), nil
}

func (p *dotfyProvider) GetCharge(ctx context.Context, id string) (ChargeResult, error) {
	data, err := p.do(ctx, http.MethodGet, "/api/charges/"+id, nil)
	if err != nil {
		return ChargeResult{}, err
	}
	var r dotfyChargeResp
	if err := json.Unmarshal(data, &r); err != nil {
		return ChargeResult{}, err
	}
	return r.toResult(), nil
}

// dotfyWebhook — formato do evento. Ajustar aos nomes reais.
type dotfyWebhook struct {
	Event string `json:"event"`
	Data  struct {
		ID            string `json:"id"`
		CorrelationID string `json:"correlationID"`
	} `json:"data"`
}

func (p *dotfyProvider) ParseWebhook(headers map[string][]string, body []byte) (WebhookEvent, error) {
	// Autentica a origem ANTES de confiar no corpo. Com secret configurado, exige
	// assinatura HMAC válida; o handler aplica fail-closed quando não há secret.
	if p.secret != "" {
		if err := p.verifyWebhookSignature(headers, body); err != nil {
			return WebhookEvent{}, err
		}
	}
	var wh dotfyWebhook
	if err := json.Unmarshal(body, &wh); err != nil {
		return WebhookEvent{}, err
	}
	if wh.Event == "" {
		return WebhookEvent{}, errors.New("evento sem tipo")
	}
	evID := wh.Data.ID
	if evID == "" {
		evID = wh.Data.CorrelationID
	}
	return WebhookEvent{
		ID:               evID + ":" + wh.Event,
		Type:             wh.Event,
		CorrelationID:    wh.Data.CorrelationID,
		ProviderChargeID: wh.Data.ID,
		Raw:              body,
	}, nil
}
