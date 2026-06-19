# payments-go — Migração do gateway PIX para Efí — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** trocar o gateway PIX do `payments-go` do Dotfy para a Efí (API Pix com mTLS), webhook-only, começando em homologação.

**Architecture:** a Efí entra como a única implementação da interface `PaymentProvider`. Novo arquivo `efi.go` (auth OAuth2 sobre mTLS + token cache em memória, `CreateCharge` via `PUT /v2/cob/{txid}`, `GetCharge`, `ParseWebhook` → `[]WebhookEvent`, `RegisterWebhook`). O cutover muda a interface (`ParseWebhook` passa a devolver slice), reescreve o handler de webhook (loop + segredo na URL), remove o Dotfy por completo e reconfigura env/main/rotas. Spec: `docs/superpowers/specs/2026-06-19-payments-go-efi-pix-design.md`.

**Tech Stack:** Go 1.25, `net/http` stdlib, `crypto/tls` (mTLS), `software.sslmate.com/src/go-pkcs12` (parse do `.p12`), `httptest` nos testes.

## Global Constraints

- **Diretório de trabalho:** `apps/payments-go` (module `github.com/santos-tech/payments`).
- **Go binário:** em `~/.local/bin` — todo comando go roda com `PATH=$PATH:$HOME/.local/bin`.
- **Gate de pré-commit OBRIGATÓRIO** (CLAUDE.md): `gofmt -l .` (vazio), `go vet ./...`, `go build ./...`, `go test ./...` — tudo verde antes de cada commit.
- **Sem `Co-Authored-By`** nos commits (preferência do Guilherme).
- **Segredos:** nunca comitar `.env` nem valores reais. `apps/payments-go/.env` já é gitignored e tem as credenciais de homolog.
- **txid da Efí:** 26–35 caracteres `[a-zA-Z0-9]` (sem `_`).
- **Valores monetários:** Efí espera reais com 2 casas decimais como string (ex.: `"10.00"`).
- **Rota do webhook:** `POST /webhooks/efi/pix` (a Efí anexa `/pix` à URL registrada `.../webhooks/efi`).
- **Branch:** `feat/payments-efi-pix` (já criado).

---

### Task 1: Dependência pkcs12 + carregador do certificado mTLS

**Files:**
- Modify: `apps/payments-go/go.mod`, `apps/payments-go/go.sum` (via `go get`)
- Create: `apps/payments-go/efi.go` (apenas o carregador do cert nesta task)
- Test: `apps/payments-go/efi_test.go`

**Interfaces:**
- Produces: `func loadClientCert(p12Base64, password string) (tls.Certificate, error)` — decodifica base64, parseia o `.p12` e devolve um `tls.Certificate` pronto para `tls.Config.Certificates`.

- [ ] **Step 1: Adicionar a dependência pkcs12**

Run:
```bash
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go get software.sslmate.com/src/go-pkcs12@latest
```
Expected: `go.mod`/`go.sum` atualizados com `software.sslmate.com/src/go-pkcs12`.

- [ ] **Step 2: Escrever o teste do carregador (falha)**

Cria `apps/payments-go/efi_test.go`:
```go
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// genTestP12Base64 gera um par chave/cert self-signed e devolve um .p12 (senha vazia) em base64.
func genTestP12Base64(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-efi"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	raw, err := pkcs12.Modern.Encode(key, cert, nil, "")
	if err != nil {
		t.Fatalf("encode p12: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestLoadClientCert(t *testing.T) {
	b64 := genTestP12Base64(t)
	cert, err := loadClientCert(b64, "")
	if err != nil {
		t.Fatalf("loadClientCert: %v", err)
	}
	if cert.Leaf == nil || cert.Leaf.Subject.CommonName != "test-efi" {
		t.Fatalf("cert sem leaf esperado: %+v", cert.Leaf)
	}
}

func TestLoadClientCertRejeitaBase64Invalido(t *testing.T) {
	if _, err := loadClientCert("não é base64 @@@", ""); err == nil {
		t.Fatal("esperava erro com base64 inválido")
	}
}
```

- [ ] **Step 3: Rodar o teste (deve falhar por `loadClientCert` indefinido)**

Run:
```bash
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... -run TestLoadClientCert 2>&1 | head
```
Expected: FAIL — `undefined: loadClientCert`.

- [ ] **Step 4: Implementar o carregador**

Cria `apps/payments-go/efi.go`:
```go
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"

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
	// Garante que a chave casa com o leaf (e popula campos internos do tls).
	if _, err := x509.ParseCertificate(leaf.Raw); err != nil {
		return tls.Certificate{}, fmt.Errorf("efi: certificado inválido: %w", err)
	}
	return cert, nil
}
```

- [ ] **Step 5: Rodar o teste (deve passar)**

Run:
```bash
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... -run TestLoadClientCert -v 2>&1 | tail
```
Expected: PASS nos dois testes.

- [ ] **Step 6: Gate + commit**

Run:
```bash
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'
git add apps/payments-go/efi.go apps/payments-go/efi_test.go apps/payments-go/go.mod apps/payments-go/go.sum
git commit -m "feat(payments-go): carregador do certificado mTLS da Efí (.p12 base64)"
```
Expected: gate verde; commit feito.

---

### Task 2: Config — adicionar campos EFI_* (aditivo, Dotfy ainda presente)

**Files:**
- Modify: `apps/payments-go/config.go`
- Test: `apps/payments-go/config_test.go` (criar se não existir)

**Interfaces:**
- Produces: campos no `Config` — `EFIBaseURL`, `EFIClientID`, `EFIClientSecret`, `EFICertP12Base64`, `EFICertPassword`, `EFIPixKey`, `EFIWebhookSecret`, `EFIWebhookURL string`.

- [ ] **Step 1: Escrever o teste de carga das envs EFI (falha)**

Cria/edita `apps/payments-go/config_test.go`:
```go
package main

import (
	"os"
	"testing"
)

func TestLoadConfigEFIDefaults(t *testing.T) {
	// obrigatórias para o boot não chamar os.Exit
	os.Setenv("DATABASE_URL", "postgres://x")
	os.Setenv("REDIS_URL", "redis://x")
	os.Setenv("JWT_SECRET", "s")
	os.Setenv("EFI_CLIENT_ID", "cid")
	os.Setenv("EFI_CLIENT_SECRET", "csec")
	os.Setenv("EFI_CERT_P12_BASE64", "YWJj")
	os.Setenv("EFI_PIX_KEY", "chave@pix")
	defer func() {
		for _, k := range []string{"EFI_CLIENT_ID", "EFI_CLIENT_SECRET", "EFI_CERT_P12_BASE64", "EFI_PIX_KEY"} {
			os.Unsetenv(k)
		}
	}()
	cfg := LoadConfig()
	if cfg.EFIBaseURL != "https://pix-h.api.efipay.com.br" {
		t.Fatalf("default homolog esperado, veio %q", cfg.EFIBaseURL)
	}
	if cfg.EFIClientID != "cid" || cfg.EFIPixKey != "chave@pix" {
		t.Fatalf("envs EFI não carregadas: %+v", cfg)
	}
}
```

- [ ] **Step 2: Rodar (deve falhar — campos inexistentes)**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... -run TestLoadConfigEFI 2>&1 | head`
Expected: FAIL — `cfg.EFIBaseURL undefined`.

- [ ] **Step 3: Adicionar os campos e a carga no `config.go`**

No `struct Config` (após a linha `DotfyWebhookSigHdr string`), adicionar:
```go
	EFIBaseURL       string
	EFIClientID      string
	EFIClientSecret  string
	EFICertP12Base64 string
	EFICertPassword  string
	EFIPixKey        string
	EFIWebhookSecret string
	EFIWebhookURL    string // URL pública a registrar no webhook da Efí (sem ?hmac)
```
No `LoadConfig()` (após a linha `DotfyWebhookSigHdr: ...`), adicionar:
```go
		EFIBaseURL:       strings.TrimRight(getEnv("EFI_BASE_URL", "https://pix-h.api.efipay.com.br"), "/"),
		EFIClientID:      mustEnv("EFI_CLIENT_ID"),
		EFIClientSecret:  mustEnv("EFI_CLIENT_SECRET"),
		EFICertP12Base64: mustEnv("EFI_CERT_P12_BASE64"),
		EFICertPassword:  getEnv("EFI_CERT_PASSWORD", ""),
		EFIPixKey:        mustEnv("EFI_PIX_KEY"),
		EFIWebhookSecret: getEnv("EFI_WEBHOOK_SECRET", ""),
		EFIWebhookURL:    strings.TrimRight(getEnv("EFI_WEBHOOK_URL", ""), "/"),
```

- [ ] **Step 4: Rodar (deve passar)**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... -run TestLoadConfigEFI -v 2>&1 | tail`
Expected: PASS.

> Nota: o tree ainda compila porque os campos DOTFY_* continuam presentes (removidos na Task 5). O `mustEnv("DOTFY_API_KEY")` ainda existe — em testes que chamam `LoadConfig()` defina `DOTFY_API_KEY` se necessário; este teste já define as obrigatórias e o `DOTFY_API_KEY` é setado por outros testes/CI via env.

- [ ] **Step 5: Gate + commit**

Run:
```bash
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./... -run "Config|LoadConfig"'
git add apps/payments-go/config.go apps/payments-go/config_test.go
git commit -m "feat(payments-go): adicionar configuração EFI_* (aditivo)"
```
Expected: gate verde; commit.

---

### Task 3: efiProvider — auth mTLS + token cache + CreateCharge + GetCharge

**Files:**
- Modify: `apps/payments-go/efi.go`
- Test: `apps/payments-go/efi_test.go`

**Interfaces:**
- Consumes: `loadClientCert` (Task 1); campos EFI do `Config` (Task 2); `ChargeRequest`, `ChargeResult`, `ProviderError` (`provider.go`).
- Produces:
  - `type efiProvider struct { base, clientID, clientSecret, pixKey string; client *http.Client; mu sync.Mutex; token string; tokenExp time.Time }`
  - `func newEfiProvider(cfg Config) *efiProvider`
  - `func (p *efiProvider) CreateCharge(ctx, ChargeRequest) (ChargeResult, error)`
  - `func (p *efiProvider) GetCharge(ctx, providerChargeID string) (ChargeResult, error)`
  - helpers: `accessToken(ctx) (string, error)`, `do(ctx, method, path string, body any) ([]byte, error)`

- [ ] **Step 1: Escrever os testes do provider (falham)**

Adicionar a `apps/payments-go/efi_test.go`:
```go
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newTestEfi monta um efiProvider apontando para um servidor de teste (sem mTLS).
func newTestEfi(base string) *efiProvider {
	return &efiProvider{
		base:         strings.TrimRight(base, "/"),
		clientID:     "cid",
		clientSecret: "csec",
		pixKey:       "chave@pix",
		client:       &http.Client{},
	}
}

func TestEfiCreateChargeAndTokenCache(t *testing.T) {
	var tokenHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenHits, 1)
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok123", "expires_in": 3600})
	})
	mux.HandleFunc("/v2/cob/", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("auth header: %q", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"txid": strings.TrimPrefix(r.URL.Path, "/v2/cob/"),
			"status": "ATIVA",
			"pixCopiaECola": "00020126BR-CODE",
			"loc": map[string]any{"id": 42},
		})
	})
	mux.HandleFunc("/v2/loc/42/qrcode", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"qrcode": "00020126BR-CODE", "imagemQrcode": "data:image/png;base64,AAAA"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newTestEfi(srv.URL)
	req := ChargeRequest{CorrelationID: "stpay0123456789abcdef0123456789", AmountCents: 1000, Description: "x", ExpiresAt: ""}
	res, err := p.CreateCharge(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateCharge: %v", err)
	}
	if res.BRCode != "00020126BR-CODE" || res.QRCode != "data:image/png;base64,AAAA" {
		t.Fatalf("resultado inesperado: %+v", res)
	}
	if res.ProviderChargeID != req.CorrelationID {
		t.Fatalf("txid esperado %q, veio %q", req.CorrelationID, res.ProviderChargeID)
	}
	if res.Status != "pending" {
		t.Fatalf("status esperado pending, veio %q", res.Status)
	}
	// Segunda cobrança reusa o token cacheado.
	if _, err := p.CreateCharge(context.Background(), req); err != nil {
		t.Fatalf("CreateCharge 2: %v", err)
	}
	if n := atomic.LoadInt32(&tokenHits); n != 1 {
		t.Fatalf("token deveria ser buscado 1x (cache), veio %d", n)
	}
}

func TestEfiCreateChargeProviderError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
	})
	mux.HandleFunc("/v2/cob/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"nome": "valor_invalido", "mensagem": "Valor não permitido"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := newTestEfi(srv.URL)
	_, err := p.CreateCharge(context.Background(), ChargeRequest{CorrelationID: "stpay0123456789abcdef0123456789", AmountCents: 1})
	var pe *ProviderError
	if err == nil || !asProviderError(err, &pe) || pe.Message != "Valor não permitido" {
		t.Fatalf("esperava ProviderError com a mensagem da Efí, veio %v", err)
	}
}
```
Adicionar também o helper de teste (mesmo arquivo):
```go
func asProviderError(err error, target **ProviderError) bool {
	return errorsAs(err, target)
}
```
> Nota: substitua `asProviderError`/`errorsAs` por `errors.As` direto: troque o `if err == nil || !asProviderError(...)` por `if err == nil || !errors.As(err, &pe)` e importe `"errors"`. (Os helpers acima existem só para deixar o passo legível; use `errors.As`.)

- [ ] **Step 2: Rodar (deve falhar — `efiProvider`/métodos indefinidos)**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... -run TestEfi 2>&1 | head`
Expected: FAIL — `undefined: efiProvider`.

- [ ] **Step 3: Implementar o provider no `efi.go`**

Adicionar ao `apps/payments-go/efi.go` (imports: `bytes context crypto/tls encoding/json errors fmt io net/http strconv sync time`):
```go
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

func (p *efiProvider) do(ctx context.Context, method, path string, body any) ([]byte, error) {
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
	data, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		if readErr != nil {
			return nil, fmt.Errorf("efi %s %s: status %d", method, path, res.StatusCode)
		}
		// Erro da Efí: {"nome","mensagem","violacoes":[...]} — repassa a mensagem amigável.
		var ee struct {
			Nome     string `json:"nome"`
			Mensagem string `json:"mensagem"`
		}
		if json.Unmarshal(data, &ee) == nil && ee.Mensagem != "" {
			return nil, &ProviderError{Message: ee.Mensagem, Status: res.StatusCode}
		}
		return nil, fmt.Errorf("efi %s %s: status %d: %s", method, path, res.StatusCode, data)
	}
	return data, nil
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
```
No teste, ajuste o import para usar `errors.As` (remova os helpers `asProviderError`/`errorsAs` e use `errors.As(err, &pe)` com `import "errors"`).

- [ ] **Step 4: Rodar (deve passar)**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... -run TestEfi -v 2>&1 | tail -20`
Expected: PASS em `TestEfiCreateChargeAndTokenCache` e `TestEfiCreateChargeProviderError`.

- [ ] **Step 5: Gate + commit**

Run:
```bash
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'
git add apps/payments-go/efi.go apps/payments-go/efi_test.go
git commit -m "feat(payments-go): efiProvider — auth mTLS, token cache, CreateCharge/GetCharge"
```
Expected: gate verde; commit.

---

### Task 4: efiProvider.ParseWebhook (→ []WebhookEvent) + RegisterWebhook

**Files:**
- Modify: `apps/payments-go/efi.go`
- Test: `apps/payments-go/efi_test.go`

**Interfaces:**
- Produces:
  - `func (p *efiProvider) ParseWebhook(headers map[string][]string, body []byte) ([]WebhookEvent, error)` — mapeia `{"pix":[...]}` em N eventos `CHARGE_PAID`; corpo sem `pix[]` (ping de teste) → slice vazio, sem erro.
  - `func (p *efiProvider) RegisterWebhook(ctx context.Context, webhookURL string) error` — `PUT /v2/webhook/{chave}` com header `x-skip-mtls-checking: true`.

- [ ] **Step 1: Escrever os testes (falham)**

Adicionar a `apps/payments-go/efi_test.go`:
```go
func TestEfiParseWebhookMultiplosPix(t *testing.T) {
	p := newTestEfi("http://x")
	body := []byte(`{"pix":[{"endToEndId":"E1","txid":"stpayAAA"},{"endToEndId":"E2","txid":"stpayBBB"}]}`)
	evs, err := p.ParseWebhook(nil, body)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("esperava 2 eventos, veio %d", len(evs))
	}
	if evs[0].Type != "CHARGE_PAID" || evs[0].ID != "E1" || evs[0].CorrelationID != "stpayAAA" {
		t.Fatalf("evento 0 inesperado: %+v", evs[0])
	}
}

func TestEfiParseWebhookPingDeTeste(t *testing.T) {
	p := newTestEfi("http://x")
	evs, err := p.ParseWebhook(nil, []byte(`{"evento":"teste_webhook"}`))
	if err != nil {
		t.Fatalf("ping não deveria dar erro: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("ping deveria render 0 eventos, veio %d", len(evs))
	}
}
```

- [ ] **Step 2: Rodar (deve falhar)**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... -run TestEfiParseWebhook 2>&1 | head`
Expected: FAIL — `ParseWebhook` indefinido / assinatura incompatível.

- [ ] **Step 3: Implementar `ParseWebhook` e `RegisterWebhook` no `efi.go`**

Adicionar ao `efi.go`:
```go
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
```

- [ ] **Step 4: Rodar (deve passar)**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... -run TestEfiParseWebhook -v 2>&1 | tail`
Expected: PASS.

- [ ] **Step 5: Gate + commit**

Run:
```bash
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'
git add apps/payments-go/efi.go apps/payments-go/efi_test.go
git commit -m "feat(payments-go): efiProvider ParseWebhook (multi-pix) e RegisterWebhook"
```
Expected: gate verde; commit.

> Após esta task, `efiProvider` tem TODOS os métodos, mas a interface `PaymentProvider` ainda declara `ParseWebhook` devolvendo um único `WebhookEvent` (Dotfy). A Task 5 faz o cutover atômico.

---

### Task 5: Cutover — trocar interface, handler, main, rotas, charges; remover Dotfy

**Files:**
- Modify: `apps/payments-go/provider.go` (assinatura `ParseWebhook`)
- Modify: `apps/payments-go/handlers_webhook.go` (renomear + loop + segredo)
- Modify: `apps/payments-go/handlers_charges.go` (`newCorrelationID`, `Provider`)
- Modify: `apps/payments-go/server.go` (rota webhook)
- Modify: `apps/payments-go/main.go` (`newEfiProvider`, fail-closed EFI, flag `-register-webhook`)
- Modify: `apps/payments-go/config.go` (remover campos/carga `Dotfy*`)
- Delete: `apps/payments-go/dotfy.go`, `apps/payments-go/dotfy_test.go`
- Test: `apps/payments-go/handlers_webhook_test.go` (criar)

**Interfaces:**
- Consumes: `efiProvider` + métodos (Tasks 3–4); `headerValue` (estava em `dotfy.go` — **mover** para `efi.go` ou `util`-like, ver Step 6).
- Produces: `PaymentProvider.ParseWebhook(headers, body) ([]WebhookEvent, error)`; `Server.handleWebhook`.

- [ ] **Step 1: Escrever o teste do handler de webhook (falha)**

Cria `apps/payments-go/handlers_webhook_test.go`:
```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// efiStub satisfaz PaymentProvider só para o handler (store nil → caminho de teste).
type efiStub struct{}

func (efiStub) CreateCharge(_ contextWrap, _ ChargeRequest) (ChargeResult, error) { return ChargeResult{}, nil }
func (efiStub) GetCharge(_ contextWrap, _ string) (ChargeResult, error)           { return ChargeResult{}, nil }
func (efiStub) ParseWebhook(_ map[string][]string, body []byte) ([]WebhookEvent, error) {
	if strings.Contains(string(body), "stpay") {
		return []WebhookEvent{{ID: "E1", Type: "CHARGE_PAID", CorrelationID: "stpayAAA"}}, nil
	}
	return nil, nil
}

func TestHandleWebhookSegredoErrado(t *testing.T) {
	s := &Server{cfg: Config{Production: true, EFIWebhookSecret: "right"}, provider: efiStub{}}
	r := httptest.NewRequest("POST", "/webhooks/efi/pix?hmac=wrong", strings.NewReader(`{"pix":[]}`))
	w := httptest.NewRecorder()
	s.handleWebhook(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("segredo errado deveria dar 401, veio %d", w.Code)
	}
}

func TestHandleWebhookSegredoCerto(t *testing.T) {
	s := &Server{cfg: Config{EFIWebhookSecret: "right"}, provider: efiStub{}} // store nil → 200 cedo
	r := httptest.NewRequest("POST", "/webhooks/efi/pix?hmac=right", strings.NewReader(`{"pix":[{"txid":"stpayAAA"}]}`))
	w := httptest.NewRecorder()
	s.handleWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("segredo certo deveria dar 200, veio %d", w.Code)
	}
}
```
> O alias `contextWrap` no stub é só para evitar importar `context` no exemplo; **na implementação real do teste, importe `context` e use `context.Context`** nas assinaturas de `CreateCharge`/`GetCharge`. Ajuste o stub para `context.Context`.

- [ ] **Step 2: Rodar (deve falhar)**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... -run TestHandleWebhook 2>&1 | head`
Expected: FAIL — `handleWebhook` indefinido / interface incompatível.

- [ ] **Step 3: Mudar a interface em `provider.go`**

Trocar a linha:
```go
	ParseWebhook(headers map[string][]string, body []byte) (WebhookEvent, error)
```
por:
```go
	ParseWebhook(headers map[string][]string, body []byte) ([]WebhookEvent, error)
```

- [ ] **Step 4: Reescrever `handlers_webhook.go` (topo do arquivo)**

Substituir a função `handleDotfyWebhook` (até a linha `writeJSON(w, http.StatusOK, map[string]bool{"ok": true})` final do switch) por:
```go
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Sem o mTLS de volta (skip), autenticamos pelo segredo na URL que só a Efí
	// conhece (nós o registramos). Fail-closed em produção: sem secret, recusa.
	if s.cfg.Production && s.cfg.EFIWebhookSecret == "" {
		slog.Error("webhook recusado: EFI_WEBHOOK_SECRET ausente em produção")
		writeError(w, http.StatusServiceUnavailable, "webhook_unverifiable", "Webhook não verificável")
		return
	}
	if s.cfg.EFIWebhookSecret != "" {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("hmac")), []byte(s.cfg.EFIWebhookSecret)) != 1 {
			slog.Warn("webhook efi rejeitado: segredo inválido")
			writeError(w, http.StatusUnauthorized, "webhook_rejected", "Webhook não autenticado")
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Corpo inválido")
		return
	}
	evs, err := s.provider.ParseWebhook(r.Header, body)
	if err != nil {
		slog.Warn("webhook efi: payload inválido", "err", err, "body_len", len(body))
		writeError(w, http.StatusBadRequest, "invalid_body", "Payload inválido")
		return
	}
	if s.store == nil { // guarda defensiva (testes)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	for _, ev := range evs {
		fresh, err := s.store.MarkWebhookSeen(r.Context(), ev.ID, ev.Type, ev.Raw)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar evento")
			return
		}
		if !fresh {
			continue // já processado
		}
		switch ev.Type {
		case "CHARGE_PAID":
			if err := s.store.MarkChargePaid(r.Context(), ev.CorrelationID); err != nil {
				slog.Warn("falha ao marcar paga", "corr", ev.CorrelationID, "err", err)
			} else if tok, e := s.store.PublicTokenByCorrelation(r.Context(), ev.CorrelationID); e == nil {
				s.invalidateChargeStatus(r.Context(), tok)
				s.publishChargePaid(r.Context(), tok)
				s.enqueueNotifyPaid(r.Context(), tok)
			}
		default:
			slog.Info("evento efi ignorado", "type", ev.Type)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
```
Atualizar os imports do arquivo: adicionar `"crypto/subtle"`; remover `"time"` se ficar sem uso (verificar — `notifyPaidInline` usa `time`, então mantém).

- [ ] **Step 5: Atualizar `handlers_charges.go`**

Trocar `newCorrelationID`:
```go
func newCorrelationID() string {
	b := make([]byte, 14)
	_, _ = rand.Read(b)
	return "stpay" + hex.EncodeToString(b) // 5 + 28 = 33 chars alnum (txid Efí: 26–35)
}
```
Trocar `Provider: "dotfy"` (linha ~64) por `Provider: "efi"`.

- [ ] **Step 6: Mover `headerValue` para `efi.go`** (estava em `dotfy.go`, ainda usado?)

Verificar uso: `cd apps/payments-go && grep -rn "headerValue" *.go`. Se algum arquivo (fora de `dotfy*.go`) usar, mover a função `headerValue` (e o import `net/textproto`) de `dotfy.go` para `efi.go`. Se ninguém mais usar, não mover (será apagada com o Dotfy).

- [ ] **Step 7: Atualizar `server.go`**

Trocar:
```go
	mux.HandleFunc("POST /webhooks/dotfy", s.handleDotfyWebhook)
```
por:
```go
	mux.HandleFunc("POST /webhooks/efi/pix", s.handleWebhook)
```

- [ ] **Step 8: Atualizar `main.go`**

Trocar `provider := newDotfyProvider(cfg)` por `provider := newEfiProvider(cfg)`.
Trocar o bloco fail-closed:
```go
	if cfg.Production && cfg.DotfyWebhookSecret == "" {
		slog.Error("DOTFY_WEBHOOK_SECRET ausente em produção: webhooks serão RECUSADOS até configurar")
	}
```
por:
```go
	if cfg.Production && cfg.EFIWebhookSecret == "" {
		slog.Error("EFI_WEBHOOK_SECRET ausente em produção: webhooks serão RECUSADOS até configurar")
	}
```
Adicionar o subcomando `-register-webhook` logo após `cfg := LoadConfig()` (antes de conectar Postgres/Redis), incluindo `import "flag"`:
```go
	registerWebhook := flag.Bool("register-webhook", false, "registra o webhook na Efí e sai")
	flag.Parse()
	if *registerWebhook {
		if cfg.EFIWebhookURL == "" {
			slog.Error("EFI_WEBHOOK_URL ausente — defina a URL pública do webhook")
			os.Exit(1)
		}
		url := cfg.EFIWebhookURL
		if cfg.EFIWebhookSecret != "" {
			url += "?hmac=" + cfg.EFIWebhookSecret
		}
		if err := newEfiProvider(cfg).RegisterWebhook(ctx, url); err != nil {
			slog.Error("falha ao registrar webhook na Efí", "err", err)
			os.Exit(1)
		}
		slog.Info("webhook registrado na Efí", "url", cfg.EFIWebhookURL)
		return
	}
```
> `ctx` já existe logo abaixo (`ctx := context.Background()`); mova a declaração de `ctx` para ANTES deste bloco (ou use `context.Background()` direto na chamada).

- [ ] **Step 9: Remover a config do Dotfy em `config.go`**

Remover do struct os campos `DotfyBaseURL`, `DotfyAPIKey`, `DotfyWebhookSecret`, `DotfyWebhookSigHdr`. Remover de `LoadConfig()` as 4 linhas `Dotfy...:`.

- [ ] **Step 10: Apagar o Dotfy**

Run:
```bash
cd apps/payments-go && git rm dotfy.go dotfy_test.go
```

- [ ] **Step 11: Ajustar o stub de teste para `context.Context`**

Em `handlers_webhook_test.go`, importar `"context"` e trocar `contextWrap` por `context.Context` nas assinaturas do `efiStub`.

- [ ] **Step 12: Rodar tudo (deve passar)**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... 2>&1 | tail -20`
Expected: PASS — incluindo `TestHandleWebhookSegredoErrado/Certo` e os testes existentes. Se algum teste antigo referenciava `DOTFY_*` ou `handleDotfyWebhook`, ajustá-lo aqui.

- [ ] **Step 13: Gate + commit**

Run:
```bash
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'
git add -A apps/payments-go
git commit -m "feat(payments-go): cutover para Efí (interface multi-evento, webhook, remoção do Dotfy)"
```
Expected: gate verde; commit.

---

### Task 6: `.env.example`, OpenAPI e llms.txt

**Files:**
- Modify: `apps/payments-go/.env.example`
- Modify: `docs/openapi-payments.yaml`
- Modify: `apps/api-go/llms.txt`

- [ ] **Step 1: Atualizar `.env.example`**

Remover linhas `DOTFY_*`. Adicionar (placeholders, sem valores reais):
```
# Efí (gateway PIX) — homologação por default
EFI_BASE_URL=https://pix-h.api.efipay.com.br
EFI_CLIENT_ID=
EFI_CLIENT_SECRET=
EFI_CERT_P12_BASE64=
EFI_CERT_PASSWORD=
EFI_PIX_KEY=
EFI_WEBHOOK_SECRET=
EFI_WEBHOOK_URL=https://pay.santos-tech.com/webhooks/efi
```

- [ ] **Step 2: Atualizar `docs/openapi-payments.yaml`**

Localizar o path do webhook (`/webhooks/dotfy`) e renomear para `/webhooks/efi/pix`. Ajustar `summary`/`description` mencionando a Efí e o parâmetro de query `hmac` (segredo). Se não houver path de webhook documentado, adicionar um mínimo descrevendo `POST /webhooks/efi/pix`.

- [ ] **Step 3: Atualizar `apps/api-go/llms.txt`**

`grep -n "dotfy\|payments\|webhook" apps/api-go/llms.txt`. Onde houver menção ao provider/webhook de pagamentos, trocar Dotfy → Efí e a rota para `/webhooks/efi/pix`.

- [ ] **Step 4: Gate + commit**

Run:
```bash
cd /home/guilherme/projetos/sg/santos-tech-infra
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'
cd /home/guilherme/projetos/sg/santos-tech-infra
git add apps/payments-go/.env.example docs/openapi-payments.yaml apps/api-go/llms.txt
git commit -m "docs(payments-go): documentar webhook/env da Efí e remover Dotfy"
```
Expected: gate verde; commit.

---

### Task 7: Smoke test em homologação (manual, com `.env`)

> Validação de runtime contra a Efí homolog. Usa `apps/payments-go/.env` (gitignored, já com as credenciais). Não há commit; é um checklist.

- [ ] **Step 1: Subir Postgres+Redis e a API local**

```bash
cd /home/guilherme/projetos/sg/santos-tech-infra
docker compose -f infra/docker-compose.yml up -d postgres redis
set -a; . apps/payments-go/.env; set +a
export DATABASE_URL=... REDIS_URL=... JWT_SECRET=...   # valores locais
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go run . &
```

- [ ] **Step 2: Registrar o webhook (one-off)**

Defina `EFI_WEBHOOK_URL` para uma URL pública alcançável pela Efí (ex.: túnel/ngrok em dev, ou `https://pay.santos-tech.com/webhooks/efi` em ambiente real), então:
```bash
cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go run . -register-webhook
```
Expected: log `webhook registrado na Efí`.

- [ ] **Step 3: Criar uma cobrança e validar o BRCode/QR**

`POST /charges` (admin) com um aluno válido; conferir que a resposta traz `brCode`/`qrCode` e que a cobrança existe na Efí (`GET /v2/cob/{txid}` via `GetCharge`/painel).

- [ ] **Step 4: Simular pagamento no sandbox da Efí**

Usar a simulação de pagamento Pix do ambiente de homologação da Efí sobre o `txid` criado; confirmar que o webhook chega em `/webhooks/efi/pix?hmac=...`, o status vira `paid` e o SSE da tela de pagamento acorda.

- [ ] **Step 5: Registrar o resultado**

Anotar no PR o que funcionou (BRCode gerado, webhook recebido, status→paid) e qualquer ajuste necessário no payload real da Efí (nomes de campos podem diferir levemente do assumido).

---

## Self-Review

**Spec coverage:**
- Arquitetura (efiProvider única impl, Dotfy removido) → Tasks 3–5. ✓
- Auth & mTLS (OAuth2 client_credentials, token cache, .p12 base64) → Tasks 1, 3. ✓
- Cert no deploy (base64 env) → Tasks 1, 2, 6. ✓
- CreateCharge/GetCharge (PUT /v2/cob/{txid}, QR via /v2/loc, status, ProviderError) → Task 3. ✓
- Ajustes em handlers_charges (newCorrelationID alnum 33, Provider "efi", override) → Task 5. ✓
- Webhook (skip-mtls, segredo na URL, payload pix[], multi-evento, ping de teste, register one-off) → Tasks 4, 5. ✓
- Config/env (EFI_*, remoção DOTFY_*) → Tasks 2, 5, 6. ✓
- Testes (httptest: token cache, CreateCharge, ProviderError, ParseWebhook single/multi/ping/segredo) → Tasks 3, 4, 5. ✓
- Deploy/docs (rota, openapi, llms, .env.example) → Task 6. ✓
- Homologação smoke → Task 7. ✓

**Placeholder scan:** sem TBD/TODO funcionais; os pontos "verificar/ajustar" (Steps 5/6/12 da Task 5, payload real na Task 7) são checagens deliberadas contra o código existente e a API real, com instrução explícita do que fazer.

**Type consistency:** `ParseWebhook` → `([]WebhookEvent, error)` consistente entre `provider.go` (Task 5), `efiProvider` (Task 4) e o handler (Task 5). `ChargeResult` campos (`BRCode/QRCode/ProviderChargeID/Status/CorrelationID`) batem com `provider.go`. `newCorrelationID` produz txid alnum usado como `req.CorrelationID` → path `/v2/cob/{txid}`.

**Risco conhecido (Task 7):** os nomes exatos de alguns campos da resposta da Efí (`pixCopiaECola`, `loc.id`, `imagemQrcode`, status) são os documentados, mas podem variar — por isso o smoke test valida contra a API real antes do merge.
