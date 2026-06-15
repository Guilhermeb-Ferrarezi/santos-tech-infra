package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type dotfyProvider struct {
	base   string
	key    string
	secret string // webhook secret (opcional)
	client *http.Client
}

func newDotfyProvider(cfg Config) *dotfyProvider {
	return &dotfyProvider{
		base:   cfg.DotfyBaseURL,
		key:    cfg.DotfyAPIKey,
		secret: cfg.DotfyWebhookSecret,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// dotfyChargeReq — formato do request. Ajustar aos nomes reais confirmados na sandbox.
type dotfyChargeReq struct {
	CorrelationID string `json:"correlationID"`
	Amount        int64  `json:"amount"` // centavos
	PayerName     string `json:"payerName,omitempty"`
	PayerTaxID    string `json:"payerTaxId,omitempty"`
	Description   string `json:"description,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
}

type dotfyChargeResp struct {
	ID      string `json:"id"`
	BRCode  string `json:"brCode"`
	QRImage string `json:"qrCodeImage"`
	Status  string `json:"status"`
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
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("dotfy %s %s: status %d: %s", method, path, res.StatusCode, data)
	}
	return data, nil
}

func (p *dotfyProvider) CreateCharge(ctx context.Context, req ChargeRequest) (ChargeResult, error) {
	data, err := p.do(ctx, http.MethodPost, "/api/charges", dotfyChargeReq{
		CorrelationID: req.CorrelationID,
		Amount:        req.AmountCents,
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
	return ChargeResult{ProviderChargeID: r.ID, BRCode: r.BRCode, QRCode: r.QRImage, Status: r.Status}, nil
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
	return ChargeResult{ProviderChargeID: r.ID, BRCode: r.BRCode, QRCode: r.QRImage, Status: r.Status}, nil
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
	// TODO ao confirmar: validar assinatura via p.secret se o Dotfy enviar header (ex: X-Signature HMAC).
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
