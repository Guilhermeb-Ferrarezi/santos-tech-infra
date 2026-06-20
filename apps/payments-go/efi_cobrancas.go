package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// efiCobrancas fala com a API Cobranças do Efí (boleto), que é um produto distinto da
// API Pix: base URL própria, Basic Auth via /v1/authorize e SEM certificado mTLS.
// Reusa o mesmo client_id/secret da Aplicação (que tem Pix e Cobranças habilitadas).
type efiCobrancas struct {
	base         string
	clientID     string
	clientSecret string
	client       *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

func newEfiCobrancas(cfg Config) *efiCobrancas {
	return &efiCobrancas{
		base:         cfg.EFICobrBaseURL,
		clientID:     cfg.EFIClientID,
		clientSecret: cfg.EFIClientSecret,
		client:       &http.Client{Timeout: 20 * time.Second}, // sem mTLS (API Cobranças)
	}
}

// accessToken devolve um token válido, renovando ~30s antes de expirar. A Cobranças usa
// POST /v1/authorize com Basic Auth e devolve access_token/expires_in (em segundos).
func (p *efiCobrancas) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.tokenExp.Add(-30*time.Second)) {
		return p.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/v1/authorize",
		bytes.NewReader([]byte(`{"grant_type":"client_credentials"}`)))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(p.clientID, p.clientSecret)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("efi cobr authorize: status %d: %s", res.StatusCode, data)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", errors.New("efi cobr authorize: access_token vazio")
	}
	p.token = tr.AccessToken
	p.tokenExp = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return p.token, nil
}

// do executa um request autenticado na API Cobranças. Erros da Efí vêm em
// {"code","error","error_description"} — repassamos como ProviderError quando há mensagem.
func (p *efiCobrancas) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	tok, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
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
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var ee struct {
			ErrorDescription any    `json:"error_description"`
			Error            string `json:"error"`
		}
		if json.Unmarshal(data, &ee) == nil {
			if msg := errDescToString(ee.ErrorDescription); msg != "" {
				return data, &ProviderError{Message: msg, Status: res.StatusCode}
			}
			if ee.Error != "" {
				return data, &ProviderError{Message: ee.Error, Status: res.StatusCode}
			}
		}
		return data, fmt.Errorf("efi cobr %s %s: status %d: %s", method, path, res.StatusCode, data)
	}
	return data, nil
}

// errDescToString extrai a mensagem amigável do error_description, que a Cobranças ora
// devolve como string, ora como objeto ({"property":...,"message":...}).
func errDescToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if m, ok := t["message"].(string); ok {
			return m
		}
	}
	return ""
}

// CreateBoleto emite um boleto (POST /v1/charge/one-step). Mínimo exigido pelo Efí:
// item (nome+valor) e customer (name+cpf); e-mail/telefone são opcionais.
func (p *efiCobrancas) CreateBoleto(ctx context.Context, req BoletoRequest) (BoletoResult, error) {
	name := req.Description
	if name == "" {
		name = "Cobrança"
	}
	customer := map[string]any{"name": req.PayerName, "cpf": req.PayerTaxID}
	if req.PayerEmail != "" {
		customer["email"] = req.PayerEmail
	}
	if req.PayerPhone != "" {
		customer["phone_number"] = req.PayerPhone
	}
	billet := map[string]any{"customer": customer, "expire_at": req.DueDate}
	metadata := map[string]any{}
	if req.CustomID != "" {
		metadata["custom_id"] = req.CustomID
	}
	if req.NotificationURL != "" {
		metadata["notification_url"] = req.NotificationURL
	}
	payload := map[string]any{
		"items":   []map[string]any{{"name": name, "value": req.AmountCents, "amount": 1}},
		"payment": map[string]any{"banking_billet": billet},
	}
	if len(metadata) > 0 {
		payload["metadata"] = metadata
	}
	data, err := p.do(ctx, http.MethodPost, "/v1/charge/one-step", payload)
	if err != nil {
		return BoletoResult{}, err
	}
	var resp struct {
		Data struct {
			Barcode  string `json:"barcode"`
			ChargeID int64  `json:"charge_id"`
			Status   string `json:"status"`
			PDF      struct {
				Charge string `json:"charge"`
			} `json:"pdf"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return BoletoResult{}, err
	}
	if resp.Data.ChargeID == 0 {
		return BoletoResult{}, fmt.Errorf("efi cobr: resposta sem charge_id: %s", data)
	}
	return BoletoResult{
		ChargeID: strconv.FormatInt(resp.Data.ChargeID, 10),
		Line:     resp.Data.Barcode,
		PDFURL:   resp.Data.PDF.Charge,
		Status:   resp.Data.Status,
	}, nil
}

// CancelBoleto cancela um boleto pendente (PUT /v1/charge/{id}/cancel, sem corpo).
func (p *efiCobrancas) CancelBoleto(ctx context.Context, chargeID string) error {
	_, err := p.do(ctx, http.MethodPut, "/v1/charge/"+chargeID+"/cancel", nil)
	return err
}

// GetNotification busca o detalhe de uma notificação (GET /v1/notification/{token}) e
// devolve os eventos de mudança de status das cobranças afetadas.
func (p *efiCobrancas) GetNotification(ctx context.Context, token string) ([]BoletoEvent, error) {
	data, err := p.do(ctx, http.MethodGet, "/v1/notification/"+token, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			Identifiers struct {
				ChargeID int64 `json:"charge_id"`
			} `json:"identifiers"`
			Status struct {
				Current string `json:"current"`
			} `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	out := make([]BoletoEvent, 0, len(resp.Data))
	for _, e := range resp.Data {
		if e.Identifiers.ChargeID == 0 {
			continue
		}
		out = append(out, BoletoEvent{
			ChargeID: strconv.FormatInt(e.Identifiers.ChargeID, 10),
			Status:   e.Status.Current,
		})
	}
	return out, nil
}
