package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// loadClientCert decodifica o .p12 (base64) e monta um tls.Certificate pronto para
// o mTLS da Efí. O .p12 da Efí costuma vir com senha vazia.
func loadClientCert(p12Base64, password string) (tls.Certificate, error) {
	raw, err := base64.StdEncoding.DecodeString(p12Base64)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("efi: base64 do certificado inválido: %w", err)
	}
	key, leaf, err := pkcs12.Decode(raw, password)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("efi: falha ao parsear .p12: %w", err)
	}
	cert := tls.Certificate{
		Certificate: [][]byte{leaf.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}
	return cert, nil
}

type efiProvider struct {
	base         string
	clientID     string
	clientSecret string
	pixKey       string
	client       *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

func newEfiProvider(cfg Config) *efiProvider {
	cert, err := loadClientCert(cfg.EFICertP12Base64, cfg.EFICertPassword)
	if err != nil {
		// Boot deve falhar cedo: sem cert não há API Pix.
		panic(fmt.Sprintf("efi: %v", err))
	}
	return &efiProvider{
		base:         cfg.EFIBaseURL,
		clientID:     cfg.EFIClientID,
		clientSecret: cfg.EFIClientSecret,
		pixKey:       cfg.EFIPixKey,
		client: &http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}}},
		},
	}
}

// accessToken devolve um token válido, renovando ~60s antes de expirar.
func (p *efiProvider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.tokenExp.Add(-60*time.Second)) {
		return p.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/oauth/token",
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
		return "", fmt.Errorf("efi oauth: status %d: %s", res.StatusCode, data)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", errors.New("efi oauth: access_token vazio")
	}
	p.token = tr.AccessToken
	p.tokenExp = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return p.token, nil
}

// doRaw executa um request autenticado e devolve status HTTP, content-type e body.
// Erros de rede e status >= 300 são tratados aqui (exceção: 202 não é erro).
// Os callers que precisam distinguir 200 vs 202 (relatórios) usam doRaw diretamente;
// os demais usam do(), que delega aqui e trata 202 como erro.
func (p *efiProvider) doRaw(ctx context.Context, method, path string, body any) (int, string, []byte, error) {
	tok, err := p.accessToken(ctx)
	if err != nil {
		return 0, "", nil, err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, "", nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rdr)
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer res.Body.Close()
	ct := res.Header.Get("Content-Type")
	data, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		if readErr != nil {
			return res.StatusCode, ct, nil, fmt.Errorf("efi %s %s: status %d", method, path, res.StatusCode)
		}
		// Erro da Efí: {"nome","mensagem","violacoes":[...]} — repassa a mensagem amigável.
		var ee struct {
			Nome     string `json:"nome"`
			Mensagem string `json:"mensagem"`
		}
		if json.Unmarshal(data, &ee) == nil && ee.Mensagem != "" {
			return res.StatusCode, ct, data, &ProviderError{Message: ee.Mensagem, Status: res.StatusCode}
		}
		return res.StatusCode, ct, data, fmt.Errorf("efi %s %s: status %d: %s", method, path, res.StatusCode, data)
	}
	return res.StatusCode, ct, data, nil
}

func (p *efiProvider) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	_, _, data, err := p.doRaw(ctx, method, path, body)
	return data, err
}

// RequestReport solicita a geração de um relatório de extrato de conciliação Pix.
// Retorna o ID do relatório (campo "id" da resposta 202 da Efí).
//
// Schema confirmado em homologação: tipoRegistros aceita apenas {pixRecebido:bool}.
// O campo pixEnviado não existe no schema da Efí (retorna 400 schema inválido).
// Em homolog o endpoint retorna 500 "Funcionalidade desabilitada" (endpoint existe
// mas está desativado); em produção deve retornar 202 com o ID do relatório.
func (p *efiProvider) RequestReport(ctx context.Context, dataMovimento string) (string, error) {
	payload := map[string]any{
		"dataMovimento": dataMovimento,
		"tipoRegistros": map[string]bool{
			"pixRecebido": true,
		},
	}
	_, _, data, err := p.doRaw(ctx, http.MethodPost, "/v2/gn/relatorios/extrato-conciliacao", payload)
	if err != nil {
		return "", err
	}
	var r struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("efi relatorio: parse resposta: %w", err)
	}
	if r.ID == "" {
		return "", fmt.Errorf("efi relatorio: id vazio na resposta: %s", data)
	}
	return r.ID, nil
}

// GetReport consulta o status de um relatório de extrato de conciliação.
// Retorna ("done", contentType, csvBytes, nil) quando 200 (pronto),
// ou ("processing", "", nil, nil) quando 202 (ainda processando).
func (p *efiProvider) GetReport(ctx context.Context, id string) (string, string, []byte, error) {
	status, ct, data, err := p.doRaw(ctx, http.MethodGet, "/v2/gn/relatorios/"+id, nil)
	if err != nil {
		return "", "", nil, err
	}
	if status == http.StatusAccepted {
		return "processing", "", nil, nil
	}
	return "done", ct, data, nil
}

// GetReceipt baixa o comprovante de um Pix pelo txid (= correlationID da cobrança).
// A Efí aceita e2eId|idEnvio|rtrId|txid no query; usamos txid, que já guardamos.
// Endpoint desabilitado em homologação; formato assumido como PDF cru (padrão BCB Pix). Verificar em produção (pode vir JSON com URL/base64).
func (p *efiProvider) GetReceipt(ctx context.Context, txid string) (string, []byte, error) {
	data, err := p.do(ctx, http.MethodGet, "/v2/gn/pix/comprovantes?txid="+url.QueryEscape(txid), nil)
	if err != nil {
		return "", nil, err
	}
	return "application/pdf", data, nil
}

// MEDInfraction representa uma infração MED (disputa Pix) da Efí.
type MEDInfraction struct {
	ID            string `json:"id"`
	EndToEndID    string `json:"endToEndId"`
	Status        string `json:"status"`
	Razao         string `json:"razao"`
	ValueCents    int64  `json:"valueCents"`
	DataTransacao string `json:"dataTransacao"`
}

// ListMED consulta infrações MED na janela [inicio, fim] via GET /v2/gn/infracoes.
func (p *efiProvider) ListMED(ctx context.Context, inicio, fim time.Time) ([]MEDInfraction, error) {
	q := "?inicio=" + url.QueryEscape(inicio.UTC().Format(time.RFC3339)) + "&fim=" + url.QueryEscape(fim.UTC().Format(time.RFC3339))
	data, err := p.do(ctx, http.MethodGet, "/v2/gn/infracoes"+q, nil)
	if err != nil {
		return nil, err
	}
	// A Efí devolve um array. Campos confirmados no smoke test.
	var raw []struct {
		IDInfracao    string `json:"idInfracao"`
		EndToEndID    string `json:"endToEndId"`
		Status        string `json:"status"`
		Razao         string `json:"razao"`
		Valor         string `json:"valor"`
		DataTransacao string `json:"dataTransacao"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]MEDInfraction, 0, len(raw))
	for _, x := range raw {
		cents := int64(0)
		if v, e := strconv.ParseFloat(x.Valor, 64); e == nil {
			cents = int64(math.Round(v * 100))
		}
		out = append(out, MEDInfraction{
			ID:            x.IDInfracao,
			EndToEndID:    x.EndToEndID,
			Status:        x.Status,
			Razao:         x.Razao,
			ValueCents:    cents,
			DataTransacao: x.DataTransacao,
		})
	}
	return out, nil
}

// GetBalance consulta o saldo disponível da conta Efí (em centavos).
func (p *efiProvider) GetBalance(ctx context.Context) (int64, error) {
	data, err := p.do(ctx, http.MethodGet, "/v2/gn/saldo", nil)
	if err != nil {
		return 0, err
	}
	var r struct {
		Saldo string `json:"saldo"` // reais, ex "100.00"
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(r.Saldo, 64)
	if err != nil {
		return 0, fmt.Errorf("efi saldo inválido %q: %w", r.Saldo, err)
	}
	return int64(math.Round(v * 100)), nil
}

// efiCobResp — resposta de /v2/cob/{txid}.
type efiCobResp struct {
	Txid          string `json:"txid"`
	Status        string `json:"status"`
	PixCopiaECola string `json:"pixCopiaECola"`
	Loc           struct {
		ID int64 `json:"id"`
	} `json:"loc"`
}

// efiStatusToApp mapeia o status da Efí para o vocabulário do app.
func efiStatusToApp(s string) string {
	switch s {
	case "CONCLUIDA":
		return "paid"
	case "ATIVA":
		return "pending"
	default: // REMOVIDA_PELO_USUARIO_RECEBEDOR, REMOVIDA_PELO_PSP, etc.
		return "expired"
	}
}

func (p *efiProvider) CreateCharge(ctx context.Context, req ChargeRequest) (ChargeResult, error) {
	expiracao := 3600
	if req.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, req.ExpiresAt); err == nil {
			if secs := int(time.Until(t).Seconds()); secs > 0 {
				expiracao = secs
			}
		}
	}
	valor := strconv.FormatFloat(float64(req.AmountCents)/100, 'f', 2, 64)
	cob := map[string]any{
		"calendario":         map[string]any{"expiracao": expiracao},
		"valor":              map[string]any{"original": valor},
		"chave":              p.pixKey,
		"solicitacaoPagador": req.Description,
	}
	if req.PayerTaxID != "" {
		cob["devedor"] = map[string]any{"nome": req.PayerName, "cpf": req.PayerTaxID}
	}
	data, err := p.do(ctx, http.MethodPut, "/v2/cob/"+req.CorrelationID, cob)
	if err != nil {
		return ChargeResult{}, err
	}
	var r efiCobResp
	if err := json.Unmarshal(data, &r); err != nil {
		return ChargeResult{}, err
	}
	res := ChargeResult{
		ProviderChargeID: r.Txid,
		CorrelationID:    "", // nós definimos o txid; manter o nosso no caller
		BRCode:           r.PixCopiaECola,
		Status:           efiStatusToApp(r.Status),
	}
	// Imagem do QR (best-effort): GET /v2/loc/{id}/qrcode.
	if r.Loc.ID != 0 {
		if qd, qerr := p.do(ctx, http.MethodGet, "/v2/loc/"+strconv.FormatInt(r.Loc.ID, 10)+"/qrcode", nil); qerr == nil {
			var qr struct {
				ImagemQrcode string `json:"imagemQrcode"`
			}
			if json.Unmarshal(qd, &qr) == nil {
				res.QRCode = qr.ImagemQrcode
			}
		}
	}
	return res, nil
}

func (p *efiProvider) GetCharge(ctx context.Context, providerChargeID string) (ChargeResult, error) {
	data, err := p.do(ctx, http.MethodGet, "/v2/cob/"+providerChargeID, nil)
	if err != nil {
		return ChargeResult{}, err
	}
	var r efiCobResp
	if err := json.Unmarshal(data, &r); err != nil {
		return ChargeResult{}, err
	}
	return ChargeResult{
		ProviderChargeID: r.Txid,
		BRCode:           r.PixCopiaECola,
		Status:           efiStatusToApp(r.Status),
	}, nil
}

type efiPixItem struct {
	EndToEndID string `json:"endToEndId"`
	Txid       string `json:"txid"`
}

// ParseWebhook mapeia o payload Pix da Efí ({"pix":[...]}) em eventos CHARGE_PAID.
// Corpo sem itens (ping de teste do registro) → slice vazio, sem erro.
// A autenticação (segredo na URL) é feita no handler, não aqui.
func (p *efiProvider) ParseWebhook(headers map[string][]string, body []byte) ([]WebhookEvent, error) {
	var wh struct {
		Pix []efiPixItem `json:"pix"`
	}
	if err := json.Unmarshal(body, &wh); err != nil {
		return nil, err
	}
	out := make([]WebhookEvent, 0, len(wh.Pix))
	for _, it := range wh.Pix {
		if it.Txid == "" {
			continue
		}
		id := it.EndToEndID
		if id == "" {
			id = it.Txid
		}
		out = append(out, WebhookEvent{
			ID:               id,
			Type:             "CHARGE_PAID",
			CorrelationID:    it.Txid,
			ProviderChargeID: it.Txid,
			Raw:              body,
		})
	}
	return out, nil
}

// RegisterWebhook registra a URL do webhook na chave Pix, pulando a validação mTLS
// de volta (necessário atrás de Cloudflare/Traefik).
func (p *efiProvider) RegisterWebhook(ctx context.Context, webhookURL string) error {
	tok, err := p.accessToken(ctx)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]string{"webhookUrl": webhookURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.base+"/v2/webhook/"+p.pixKey, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-skip-mtls-checking", "true")
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("efi register webhook: status %d: %s", res.StatusCode, data)
	}
	return nil
}
