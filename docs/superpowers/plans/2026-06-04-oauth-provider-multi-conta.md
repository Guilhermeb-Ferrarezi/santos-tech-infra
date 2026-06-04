# OAuth Provider + Multi-conta — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transformar o auth central (`apps/api-go`) em provedor OAuth 2.0 (authorization code + PKCE) com múltiplas contas no mesmo navegador e chooser de contas no `auth-web`.

**Architecture:** Cookie `accounts` assinado (HMAC) indexa até 5 sessões do Postgres; o fluxo OAuth guarda requisições e codes efêmeros no Redis e emite os mesmos JWTs HS256 de hoje. O `auth-web` ganha a rota `/oauth/choose` (chooser estilo Google).

**Tech Stack:** Go 1.25 (`net/http` stdlib, pgx/v5, go-redis/v9, golang-jwt/v5), React 19 + TanStack Router/Query + Tailwind 4.

**Spec:** `docs/superpowers/specs/2026-06-04-oauth-provider-multi-conta-design.md`

**Diretório de trabalho Go:** `apps/api-go` (todos os comandos `go ...` rodam lá).
**Diretório frontend:** `apps/auth-web`.

**Convenções do repo (obrigatórias):**
- Erros: `{ code, message }`, mensagem em português.
- Pré-commit Go: `gofmt -l .` (vazio), `go vet ./...`, `go build ./...`, `go test ./...`.
- Pré-commit front: `bun run build`.
- Mexeu em rota/handler → atualizar `docs/openapi.yaml` e `apps/api-go/llms.txt` (Task 12).

---

### Task 1: Helpers do cookie `accounts` (assinatura HMAC + lista de sessões)

**Files:**
- Create: `apps/api-go/accounts.go`
- Test: `apps/api-go/accounts_test.go`

- [ ] **Step 1: Write the failing tests**

Criar `apps/api-go/accounts_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

const (
	sidA = "11111111-1111-1111-1111-111111111111"
	sidB = "22222222-2222-2222-2222-222222222222"
	sidC = "33333333-3333-3333-3333-333333333333"
	sidD = "44444444-4444-4444-4444-444444444444"
	sidE = "55555555-5555-5555-5555-555555555555"
	sidF = "66666666-6666-6666-6666-666666666666"
)

func TestAccountsSignParseRoundTrip(t *testing.T) {
	ids := []string{sidA, sidB}
	v := signAccountsValue("secret", ids)
	got := parseAccountsValue("secret", v)
	if !slices.Equal(got, ids) {
		t.Fatalf("round trip: got %v, want %v", got, ids)
	}
}

func TestAccountsParseRejectsTampering(t *testing.T) {
	v := signAccountsValue("secret", []string{sidA})
	// troca o payload mantendo a assinatura
	tampered := sidB + v[len(sidA):]
	if got := parseAccountsValue("secret", tampered); got != nil {
		t.Fatalf("payload adulterado deveria ser rejeitado, veio %v", got)
	}
	// segredo errado
	if got := parseAccountsValue("outro", v); got != nil {
		t.Fatalf("segredo errado deveria ser rejeitado, veio %v", got)
	}
	// lixo
	if got := parseAccountsValue("secret", "garbage"); got != nil {
		t.Fatalf("valor inválido deveria ser rejeitado, veio %v", got)
	}
}

func TestAccountsParseRejectsNonUUID(t *testing.T) {
	v := signAccountsValue("secret", []string{"not-a-uuid"})
	if got := parseAccountsValue("secret", v); got != nil {
		t.Fatalf("id não-uuid deveria ser rejeitado, veio %v", got)
	}
}

func TestReadAccountsFromRequest(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	// sem cookie → vazio
	if ids := s.readAccounts(httptest.NewRequest("GET", "/x", nil)); ids != nil {
		t.Fatalf("sem cookie: %v", ids)
	}
	// cookie válido
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: accountsCookieName, Value: signAccountsValue("secret", []string{sidA})})
	if ids := s.readAccounts(r); !slices.Equal(ids, []string{sidA}) {
		t.Fatalf("cookie válido: %v", ids)
	}
}

func TestWriteAccountsEmptyExpiresCookie(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	w := httptest.NewRecorder()
	s.writeAccounts(w, nil)
	c := w.Result().Cookies()[0]
	if c.Name != accountsCookieName || c.MaxAge >= 0 {
		t.Fatalf("lista vazia deveria expirar o cookie: %+v", c)
	}
}

func TestAppendAccountDedupesRemovesAndCaps(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})

	// anexa removendo o sid antigo (rotação de refresh)
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: accountsCookieName, Value: signAccountsValue("secret", []string{sidA, sidB})})
	w := httptest.NewRecorder()
	got := s.appendAccount(w, r, sidC, sidA)
	if !slices.Equal(got, []string{sidB, sidC}) {
		t.Fatalf("append com remove: %v", got)
	}

	// duplicata: re-anexar um sid existente o move pro fim, sem duplicar
	r2 := httptest.NewRequest("GET", "/x", nil)
	r2.AddCookie(&http.Cookie{Name: accountsCookieName, Value: signAccountsValue("secret", []string{sidA, sidB})})
	got2 := s.appendAccount(httptest.NewRecorder(), r2, sidA)
	if !slices.Equal(got2, []string{sidB, sidA}) {
		t.Fatalf("dedupe: %v", got2)
	}

	// limite de 5: estoura pelo mais antigo
	r3 := httptest.NewRequest("GET", "/x", nil)
	r3.AddCookie(&http.Cookie{Name: accountsCookieName, Value: signAccountsValue("secret", []string{sidA, sidB, sidC, sidD, sidE})})
	got3 := s.appendAccount(httptest.NewRecorder(), r3, sidF)
	if !slices.Equal(got3, []string{sidB, sidC, sidD, sidE, sidF}) {
		t.Fatalf("cap 5: %v", got3)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd apps/api-go && go test -run 'TestAccounts|TestReadAccounts|TestWriteAccounts|TestAppendAccount' ./...`
Expected: FAIL (compile error: `signAccountsValue`, `accountsCookieName` etc. não existem)

- [ ] **Step 3: Write the implementation**

Criar `apps/api-go/accounts.go`:

```go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// Cookie "accounts": índice ASSINADO (HMAC-SHA256 com o JWT_SECRET) das sessões
// conhecidas neste navegador, pro multi-conta (chooser estilo Google). Não dá
// acesso por si só — uso/ativação sempre valida a sessão real no Postgres.
const (
	accountsCookieName = "accounts"
	maxAccounts        = 5
)

var sessionIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// signAccountsValue serializa ids como "id1|id2.<hmac-hex>".
func signAccountsValue(secret string, ids []string) string {
	payload := strings.Join(ids, "|")
	return payload + "." + accountsMAC(secret, payload)
}

// parseAccountsValue valida a assinatura e o formato dos ids; nil se inválido.
func parseAccountsValue(secret, value string) []string {
	i := strings.LastIndex(value, ".")
	if i < 0 {
		return nil
	}
	payload, sig := value[:i], value[i+1:]
	if !hmac.Equal([]byte(sig), []byte(accountsMAC(secret, payload))) {
		return nil
	}
	if payload == "" {
		return nil
	}
	ids := strings.Split(payload, "|")
	for _, id := range ids {
		if !sessionIDRe.MatchString(id) {
			return nil
		}
	}
	return ids
}

func accountsMAC(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// readAccounts lê os session ids do cookie (nil se ausente/inválido).
func (s *Server) readAccounts(r *http.Request) []string {
	c, err := r.Cookie(accountsCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	return parseAccountsValue(s.cfg.JWTSecret, c.Value)
}

// writeAccounts regrava o cookie (lista vazia → expira o cookie).
func (s *Server) writeAccounts(w http.ResponseWriter, ids []string) {
	if len(ids) == 0 {
		s.setCookie(w, accountsCookieName, "", -1)
		return
	}
	s.setCookie(w, accountsCookieName, signAccountsValue(s.cfg.JWTSecret, ids), int(refreshTTL.Seconds()))
}

// appendAccount adiciona sid ao fim da lista (removendo `remove` e duplicatas),
// com teto de maxAccounts — estoura pelo mais antigo. Devolve a lista gravada.
func (s *Server) appendAccount(w http.ResponseWriter, r *http.Request, sid string, remove ...string) []string {
	ids := s.readAccounts(r)
	out := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		if id != sid && !slices.Contains(remove, id) {
			out = append(out, id)
		}
	}
	out = append(out, sid)
	if len(out) > maxAccounts {
		out = out[len(out)-maxAccounts:]
	}
	s.writeAccounts(w, out)
	return out
}

// removeAccount tira sid da lista e regrava o cookie. Devolve a lista gravada.
func (s *Server) removeAccount(w http.ResponseWriter, r *http.Request, sid string) []string {
	ids := s.readAccounts(r)
	out := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == sid })
	s.writeAccounts(w, out)
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd apps/api-go && go test -run 'TestAccounts|TestReadAccounts|TestWriteAccounts|TestAppendAccount' ./...`
Expected: PASS

- [ ] **Step 5: gofmt + vet + commit**

```bash
cd apps/api-go && gofmt -l . && go vet ./... && go build ./...
git add apps/api-go/accounts.go apps/api-go/accounts_test.go
git commit -m "feat(api-go): cookie assinado 'accounts' para multi-conta no navegador"
```

---

### Task 2: Migração `oauth_clients` + helpers de banco (clients e sessões por id)

**Files:**
- Modify: `apps/api-go/db.go` (const `migration`, seção de sessões, novos helpers)

Sem teste unitário possível (tudo é SQL contra Postgres real); a verificação é `go build` + os testes de integração da Task 13. A migração segue o padrão idempotente existente.

- [ ] **Step 1: Adicionar a tabela à migração**

Em `apps/api-go/db.go`, dentro da const `migration` (antes do fechamento da string, após o bloco `api_keys`), adicionar:

```sql
CREATE TABLE IF NOT EXISTS oauth_clients (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id     TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  redirect_uris TEXT[] NOT NULL,
  is_active     BOOLEAN NOT NULL DEFAULT true,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- [ ] **Step 2: createSession passa a devolver o id**

Em `apps/api-go/db.go`, substituir `createSession`:

```go
// createSession grava a sessão e devolve o id (usado no cookie "accounts").
func (s *Server) createSession(ctx context.Context, userID int64, refreshHash string, expires time.Time) (string, error) {
	var id string
	err := s.db.QueryRow(ctx,
		`INSERT INTO sessions (user_id, refresh_token_hash, expires_at) VALUES ($1,$2,$3) RETURNING id::text`,
		userID, refreshHash, expires).Scan(&id)
	return id, err
}
```

(O chamador `issueSession` é ajustado na Task 3 — até lá `go build` quebra; tudo na mesma sequência de commits da Task 3.)

**Nota:** Tasks 2 e 3 commitam JUNTAS (a mudança de assinatura quebra o build no meio). Siga os steps das duas e commite no final da Task 3.

- [ ] **Step 3: Helpers de sessão por id (chooser/ativação)**

Em `apps/api-go/db.go`, após `deleteUserSessions`, adicionar:

```go
// sessionUserByID devolve o usuário de uma sessão VIVA (nil se expirada/inexistente).
func (s *Server) sessionUserByID(ctx context.Context, sessionID string) (*User, error) {
	return scanUser(s.db.QueryRow(ctx,
		`SELECT `+userCols2+` FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.id = $1::uuid AND s.expires_at > now()`, sessionID))
}

// AccountSummary é a visão de uma conta no chooser multi-conta.
type AccountSummary struct {
	SessionID string  `json:"sessionId"`
	Name      string  `json:"name"`
	Email     string  `json:"email"`
	AvatarURL *string `json:"avatarUrl"`
	Active    bool    `json:"active"`
}

// accountSummaries resolve os ids do cookie em contas vivas, preservando a
// ordem do cookie (sessões mortas simplesmente não voltam).
func (s *Server) accountSummaries(ctx context.Context, ids []string) ([]AccountSummary, error) {
	rows, err := s.db.Query(ctx,
		`SELECT s.id::text, u.name, u.email, u.avatar_url
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.id = ANY($1::uuid[]) AND s.expires_at > now() AND u.suspended_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]AccountSummary{}
	for rows.Next() {
		var a AccountSummary
		if err := rows.Scan(&a.SessionID, &a.Name, &a.Email, &a.AvatarURL); err != nil {
			return nil, err
		}
		byID[a.SessionID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]AccountSummary, 0, len(byID))
	for _, id := range ids {
		if a, ok := byID[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}
```

**Atenção ao `userCols2`:** `userCols` usa colunas sem prefixo; no JOIN acima é preciso prefixar com `u.`. Definir logo abaixo de `userCols`:

```go
// userCols com prefixo "u." pra queries com JOIN em sessions.
var userCols2 = "u." + strings.ReplaceAll(userCols, ", ", ", u.")
```

E adicionar `"strings"` aos imports de `db.go`.

**Cuidado:** `custom_role_id::text` prefixado vira `u.custom_role_id::text` — o ReplaceAll preserva isso corretamente porque o cast vem depois do nome.

- [ ] **Step 4: Helpers de oauth_clients**

Em `apps/api-go/db.go`, ao final do arquivo, adicionar:

```go
// ── OAuth clients (aplicações "Entrar com Santos Tech") ─────────────────────

type OAuthClient struct {
	ID           string    `json:"id"`
	ClientID     string    `json:"clientId"`
	Name         string    `json:"name"`
	RedirectURIs []string  `json:"redirectUris"`
	IsActive     bool      `json:"isActive"`
	CreatedAt    time.Time `json:"createdAt"`
}

const oauthClientCols = `id::text, client_id, name, redirect_uris, is_active, created_at`

func scanOAuthClient(row pgx.Row) (*OAuthClient, error) {
	var c OAuthClient
	err := row.Scan(&c.ID, &c.ClientID, &c.Name, &c.RedirectURIs, &c.IsActive, &c.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func (s *Server) oauthClientByClientID(ctx context.Context, clientID string) (*OAuthClient, error) {
	return scanOAuthClient(s.db.QueryRow(ctx,
		`SELECT `+oauthClientCols+` FROM oauth_clients WHERE client_id=$1`, clientID))
}

func (s *Server) listOAuthClients(ctx context.Context) ([]OAuthClient, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+oauthClientCols+` FROM oauth_clients ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthClient{}
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Server) insertOAuthClient(ctx context.Context, clientID, name string, uris []string) (*OAuthClient, error) {
	return scanOAuthClient(s.db.QueryRow(ctx,
		`INSERT INTO oauth_clients (client_id, name, redirect_uris) VALUES ($1,$2,$3)
		 RETURNING `+oauthClientCols, clientID, name, uris))
}

// updateOAuthClient atualiza campos não-nil (COALESCE/CASE, padrão updateUserAdmin).
func (s *Server) updateOAuthClient(ctx context.Context, id string, name *string, uris []string, active *bool) (*OAuthClient, error) {
	return scanOAuthClient(s.db.QueryRow(ctx,
		`UPDATE oauth_clients SET
		   name = COALESCE($2, name),
		   redirect_uris = COALESCE($3, redirect_uris),
		   is_active = COALESCE($4, is_active)
		 WHERE id = $1::uuid RETURNING `+oauthClientCols,
		id, name, uris, active))
}

func (s *Server) deleteOAuthClient(ctx context.Context, id string) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM oauth_clients WHERE id=$1::uuid`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
```

(Sem commit ainda — continua na Task 3.)

---

### Task 3: `issueSession` anexa a conta; logout remove

**Files:**
- Modify: `apps/api-go/handlers_auth.go` (issueSession, endSession, chamadores)
- Modify: `apps/api-go/handlers_oauth.go:103` (chamador)
- Modify: `apps/api-go/handlers_mfa.go:187` (chamador)

- [ ] **Step 1: Nova assinatura de issueSession**

Em `apps/api-go/handlers_auth.go`, substituir `issueSession`:

```go
// issueSession gera tokens, grava a sessão (refresh hash), seta os cookies e
// anexa a sessão ao cookie multi-conta "accounts". replaceSIDs: sessões antigas
// a tirar da lista (ex.: rotação de refresh).
func (s *Server) issueSession(ctx context.Context, w http.ResponseWriter, r *http.Request, u *User, replaceSIDs ...string) error {
	access, refresh, err := generateTokens(s.cfg.JWTSecret, s.cfg.JWTRefreshSecret, u.ID, u.Email)
	if err != nil {
		return err
	}
	sid, err := s.createSession(ctx, u.ID, hashRefreshToken(refresh), time.Now().Add(refreshTTL))
	if err != nil {
		return err
	}
	s.setAuthCookies(w, access, refresh)
	s.appendAccount(w, r, sid, replaceSIDs...)
	return nil
}
```

- [ ] **Step 2: Atualizar os 4 chamadores**

1. `handlers_auth.go` (handleLogin, era linha ~126):
   `if err := s.issueSession(r.Context(), w, u); err != nil {` → `if err := s.issueSession(r.Context(), w, r, u); err != nil {`
2. `handlers_auth.go` (handleRefresh, era linha ~233) — a rotação troca o sid antigo pelo novo:
   `if err := s.issueSession(r.Context(), w, u); err != nil {` → `if err := s.issueSession(r.Context(), w, r, u, sid); err != nil {`
3. `handlers_oauth.go:103` (handleGoogleCallback): mesmo ajuste do item 1.
4. `handlers_mfa.go:187` (handleMFAVerify): mesmo ajuste do item 1.

- [ ] **Step 3: Logout remove a conta da lista**

Em `apps/api-go/handlers_auth.go`, substituir `endSession`:

```go
// endSession apaga a sessão (pelo refresh cookie), limpa os cookies ativos e
// tira a conta do cookie multi-conta. As demais contas permanecem no chooser.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("refresh_token"); err == nil && c.Value != "" {
		if sid, _, _, e := s.sessionByHash(r.Context(), hashRefreshToken(c.Value)); e == nil {
			_ = s.deleteSession(r.Context(), sid)
			s.removeAccount(w, r, sid)
		}
	}
	s.clearAuthCookies(w)
}
```

- [ ] **Step 4: Build + testes existentes**

Run: `cd apps/api-go && gofmt -l . && go vet ./... && go build ./... && go test ./...`
Expected: build OK, testes existentes PASS (nenhum testava a assinatura antiga de issueSession diretamente — se algum quebrar por compilação, ajustar a chamada no teste com o mesmo padrão `w, r, u`)

- [ ] **Step 5: Commit (Tasks 2+3 juntas)**

```bash
git add apps/api-go/db.go apps/api-go/handlers_auth.go apps/api-go/handlers_oauth.go apps/api-go/handlers_mfa.go
git commit -m "feat(api-go): login anexa sessão ao cookie multi-conta; tabela oauth_clients"
```

---

### Task 4: Endpoints `/auth/accounts` (listar, remover, ativar)

**Files:**
- Create: `apps/api-go/handlers_accounts.go`
- Test: `apps/api-go/handlers_accounts_test.go`
- Modify: `apps/api-go/routes.go`

- [ ] **Step 1: Write the failing tests** (caminhos sem banco, padrão `handlers_test.go`)

Criar `apps/api-go/handlers_accounts_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleAccountsListNoCookie(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	w := httptest.NewRecorder()
	s.handleAccountsList(w, httptest.NewRequest("GET", "/auth/accounts", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"accounts":[]`) {
		t.Fatalf("esperava lista vazia, veio %s", body)
	}
}

func TestHandleAccountDeleteNotInCookie(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	r := httptest.NewRequest("DELETE", "/auth/accounts/"+sidA, nil)
	r.SetPathValue("sessionId", sidA)
	w := httptest.NewRecorder()
	s.handleAccountDelete(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("sid fora do cookie deveria dar 404, veio %d", w.Code)
	}
}

func TestHandleAccountActivateNotInCookie(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	r := httptest.NewRequest("POST", "/auth/accounts/"+sidA+"/activate", nil)
	r.SetPathValue("sessionId", sidA)
	w := httptest.NewRecorder()
	s.handleAccountActivate(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("sid fora do cookie deveria dar 401, veio %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "SESSION_EXPIRED") {
		t.Fatalf("esperava SESSION_EXPIRED, veio %s", w.Body.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd apps/api-go && go test -run TestHandleAccount ./...`
Expected: FAIL (handlers não existem)

- [ ] **Step 3: Write the implementation**

Criar `apps/api-go/handlers_accounts.go`:

```go
package main

import (
	"net/http"
	"slices"
)

// GET /auth/accounts — contas conhecidas neste navegador (cookie assinado),
// já podadas das sessões mortas. "active" = a sessão do refresh cookie atual.
func (s *Server) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	ids := s.readAccounts(r)
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"accounts": []AccountSummary{}})
		return
	}
	activeSID := ""
	if c, err := r.Cookie("refresh_token"); err == nil && c.Value != "" {
		if sid, _, _, e := s.sessionByHash(r.Context(), hashRefreshToken(c.Value)); e == nil {
			activeSID = sid
		}
	}
	accounts, err := s.accountSummaries(r.Context(), ids)
	if err != nil {
		writeErr(w, err)
		return
	}
	alive := make([]string, 0, len(accounts))
	for i := range accounts {
		accounts[i].Active = accounts[i].SessionID == activeSID
		alive = append(alive, accounts[i].SessionID)
	}
	if len(alive) != len(ids) {
		s.writeAccounts(w, alive) // auto-limpeza: mortas saem do cookie
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

// DELETE /auth/accounts/{sessionId} — tira a conta da lista e revoga a sessão.
func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sessionId")
	if !slices.Contains(s.readAccounts(r), sid) {
		writeErr(w, appErr(http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Conta não encontrada neste navegador"))
		return
	}
	_ = s.deleteSession(r.Context(), sid)
	s.removeAccount(w, r, sid)
	w.WriteHeader(http.StatusNoContent)
}

// POST /auth/accounts/{sessionId}/activate — troca a conta ativa no auth-web:
// rotaciona a sessão escolhida (a antiga é apagada) e seta os cookies ativos.
func (s *Server) handleAccountActivate(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sessionId")
	if !slices.Contains(s.readAccounts(r), sid) {
		writeErr(w, appErr(http.StatusUnauthorized, "SESSION_EXPIRED", "Sessão não encontrada neste navegador"))
		return
	}
	u, err := s.sessionUserByID(r.Context(), sid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		s.removeAccount(w, r, sid) // auto-limpeza
		writeErr(w, appErr(http.StatusUnauthorized, "SESSION_EXPIRED", "Sessão expirada — entre novamente"))
		return
	}
	if u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}
	_ = s.deleteSession(r.Context(), sid) // rotaciona: a nova sessão substitui
	if err := s.issueSession(r.Context(), w, r, u, sid); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.buildProfile(r.Context(), u)})
}
```

Em `apps/api-go/routes.go`, após o bloco de sudo (linha ~63), adicionar:

```go
	// Multi-conta no navegador (cookie assinado "accounts" — chooser estilo Google)
	mux.HandleFunc("GET /auth/accounts", s.handleAccountsList)
	mux.HandleFunc("DELETE /auth/accounts/{sessionId}", s.handleAccountDelete)
	mux.HandleFunc("POST /auth/accounts/{sessionId}/activate", s.rateLimit(20, min, s.handleAccountActivate))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd apps/api-go && go test -run TestHandleAccount ./... && go vet ./... && gofmt -l .`
Expected: PASS, vet limpo, gofmt vazio

- [ ] **Step 5: Commit**

```bash
git add apps/api-go/handlers_accounts.go apps/api-go/handlers_accounts_test.go apps/api-go/routes.go
git commit -m "feat(api-go): endpoints /auth/accounts (listar, remover, ativar conta)"
```

---

### Task 5: PKCE + tipos efêmeros do OAuth (Redis)

**Files:**
- Create: `apps/api-go/oauthprovider.go`
- Test: `apps/api-go/oauthprovider_test.go`

- [ ] **Step 1: Write the failing tests**

Criar `apps/api-go/oauthprovider_test.go`:

```go
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestVerifyPKCE(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	if !verifyPKCE(verifier, challenge) {
		t.Fatal("verifier correto deveria validar")
	}
	if verifyPKCE("outro-verifier", challenge) {
		t.Fatal("verifier errado não deveria validar")
	}
	if verifyPKCE("", challenge) || verifyPKCE(verifier, "") {
		t.Fatal("vazio não deveria validar")
	}
}

func TestAuthCodeKeyHashesCode(t *testing.T) {
	if authCodeKey("abc") == "oauth:code:abc" {
		t.Fatal("a chave deve usar o hash do code, nunca o code em claro")
	}
	if authCodeKey("abc") != "oauth:code:"+sha256Hex("abc") {
		t.Fatal("chave inesperada")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd apps/api-go && go test -run 'TestVerifyPKCE|TestAuthCodeKey' ./...`
Expected: FAIL (compile error)

- [ ] **Step 3: Write the implementation**

Criar `apps/api-go/oauthprovider.go`:

```go
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"time"
)

// Fluxo OAuth 2.0 authorization code + PKCE (S256) — só apps internos por ora.
// Estado efêmero no Redis; tokens finais são os mesmos JWTs HS256 da sessão.

// authRequest é uma autorização pendente (Redis oauth:authreq:<id>, TTL 10min).
type authRequest struct {
	ClientID      string `json:"clientId"`
	ClientName    string `json:"clientName"`
	RedirectURI   string `json:"redirectUri"`
	State         string `json:"state"`
	CodeChallenge string `json:"codeChallenge"`
}

// authCode é um code emitido (Redis oauth:code:<sha256>, TTL 60s, uso único).
type authCode struct {
	ClientID      string `json:"clientId"`
	RedirectURI   string `json:"redirectUri"`
	UserID        int64  `json:"userId"`
	CodeChallenge string `json:"codeChallenge"`
}

const (
	authReqTTL  = 10 * time.Minute
	authCodeTTL = time.Minute
)

func authReqKey(id string) string { return "oauth:authreq:" + id }

// authCodeKey guarda pelo HASH do code — vazamento do Redis não vaza codes.
func authCodeKey(code string) string { return "oauth:code:" + sha256Hex(code) }

// verifyPKCE confere challenge == base64url(sha256(verifier)), sem padding (S256).
func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd apps/api-go && go test -run 'TestVerifyPKCE|TestAuthCodeKey' ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/api-go/oauthprovider.go apps/api-go/oauthprovider_test.go
git commit -m "feat(api-go): PKCE S256 e tipos efêmeros do OAuth provider"
```

---

### Task 6: `GET /oauth/authorize`

**Files:**
- Create: `apps/api-go/handlers_oauth_provider.go`
- Test: `apps/api-go/handlers_oauth_provider_test.go`
- Modify: `apps/api-go/routes.go`

- [ ] **Step 1: Write the failing test** (validação sem banco)

Criar `apps/api-go/handlers_oauth_provider_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOAuthAuthorizeMissingParams(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	w := httptest.NewRecorder()
	s.handleOAuthAuthorize(w, httptest.NewRequest("GET", "/oauth/authorize", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("sem client_id/redirect_uri deveria dar 400, veio %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("esperava VALIDATION_ERROR, veio %s", w.Body.String())
	}
}

func TestOAuthTokenUnsupportedGrant(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	r := httptest.NewRequest("POST", "/oauth/token", strings.NewReader("grant_type=password"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleOAuthToken(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("grant_type não suportado deveria dar 400, veio %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "UNSUPPORTED_GRANT_TYPE") {
		t.Fatalf("esperava UNSUPPORTED_GRANT_TYPE, veio %s", w.Body.String())
	}
}

func TestOAuthConfirmMissingBody(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	r := httptest.NewRequest("POST", "/oauth/authorize/confirm", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.handleOAuthConfirm(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("body vazio deveria dar 400, veio %d", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/api-go && go test -run TestOAuth ./...`
Expected: FAIL (compile error — handlers ainda não existem; os 3 handlers nascem nas Tasks 6–8, então crie os três como stubs OU implemente as Tasks 6–8 em sequência antes de rodar; recomendado: escrever os 3 testes agora e implementar 6→7→8)

- [ ] **Step 3: Write the implementation**

Criar `apps/api-go/handlers_oauth_provider.go` (começa com o authorize; confirm e token entram nas Tasks 7–8):

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// GET /oauth/authorize?client_id&redirect_uri&response_type=code&state
//                      &code_challenge&code_challenge_method=S256
// Valida o client e manda o navegador pro chooser de contas do auth-web.
func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")
	if clientID == "" || redirectURI == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "client_id e redirect_uri são obrigatórios"))
		return
	}
	client, err := s.oauthClientByClientID(r.Context(), clientID)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Client/redirect inválidos → erro JSON direto: NUNCA redirecionar pra URI
	// não confiável (regra da spec OAuth).
	if client == nil || !client.IsActive || !slices.Contains(client.RedirectURIs, redirectURI) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CLIENT", "Aplicação ou redirect_uri inválidos"))
		return
	}
	// Redirect validado: demais erros voltam pro app via ?error= (spec OAuth).
	redirectErr := func(code string) {
		u, _ := url.Parse(redirectURI)
		qq := u.Query()
		qq.Set("error", code)
		if st := q.Get("state"); st != "" {
			qq.Set("state", st)
		}
		u.RawQuery = qq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		redirectErr("unsupported_response_type")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		redirectErr("invalid_request") // PKCE S256 é obrigatório
		return
	}

	id := randomToken(16)
	raw, _ := json.Marshal(authRequest{
		ClientID: clientID, ClientName: client.Name, RedirectURI: redirectURI,
		State: q.Get("state"), CodeChallenge: q.Get("code_challenge"),
	})
	if err := s.rdb.Set(r.Context(), authReqKey(id), raw, authReqTTL).Err(); err != nil {
		writeErr(w, err)
		return
	}
	http.Redirect(w, r, s.cfg.AuthWebOrigin+"/oauth/choose?request_id="+id, http.StatusFound)
}
```

Em `apps/api-go/routes.go`, após o bloco do Google OAuth, adicionar:

```go
	// OAuth provider ("Entrar com Santos Tech") — authorization code + PKCE
	mux.HandleFunc("GET /oauth/authorize", s.rateLimit(30, min, s.handleOAuthAuthorize))
	mux.HandleFunc("POST /oauth/authorize/confirm", s.rateLimit(20, min, s.handleOAuthConfirm))
	mux.HandleFunc("POST /oauth/token", s.rateLimit(20, min, s.handleOAuthToken))
```

(As rotas confirm/token só compilam após as Tasks 7–8 — implementar 6→7→8 e commitar juntas, OU comentar as duas linhas até lá. Recomendado: implementar em sequência.)

---

### Task 7: `POST /oauth/authorize/confirm`

**Files:**
- Modify: `apps/api-go/handlers_oauth_provider.go`

- [ ] **Step 1: Write the implementation**

Adicionar ao final de `apps/api-go/handlers_oauth_provider.go`:

```go
// POST /oauth/authorize/confirm {requestId, sessionId} — o usuário escolheu a
// conta no chooser; emite o code e devolve a URL de retorno pro app.
// A requisição só é consumida APÓS sucesso: se a sessão escolhida morreu, o
// usuário volta ao chooser com o MESMO request_id (UX aprovada no spec).
func (s *Server) handleOAuthConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
	}
	if err := decodeJSON(r, &body); err != nil || body.RequestID == "" || body.SessionID == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "requestId e sessionId são obrigatórios"))
		return
	}
	raw, err := s.rdb.Get(r.Context(), authReqKey(body.RequestID)).Bytes()
	if err != nil {
		writeErr(w, appErr(http.StatusGone, "REQUEST_EXPIRED", "Autorização expirou — recomece a partir do app"))
		return
	}
	var req authRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeErr(w, err)
		return
	}

	if !slices.Contains(s.readAccounts(r), body.SessionID) {
		writeErr(w, appErr(http.StatusUnauthorized, "SESSION_EXPIRED", "Sessão não encontrada neste navegador"))
		return
	}
	u, err := s.sessionUserByID(r.Context(), body.SessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		s.removeAccount(w, r, body.SessionID) // auto-limpeza do cookie
		writeErr(w, appErr(http.StatusUnauthorized, "SESSION_EXPIRED", "Sessão expirada — entre novamente"))
		return
	}
	if u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}

	code := randomToken(32)
	rawCode, _ := json.Marshal(authCode{
		ClientID: req.ClientID, RedirectURI: req.RedirectURI,
		UserID: u.ID, CodeChallenge: req.CodeChallenge,
	})
	if err := s.rdb.Set(r.Context(), authCodeKey(code), rawCode, authCodeTTL).Err(); err != nil {
		writeErr(w, err)
		return
	}
	s.rdb.Del(r.Context(), authReqKey(body.RequestID)) // consome só após sucesso

	dest, _ := url.Parse(req.RedirectURI)
	qq := dest.Query()
	qq.Set("code", code)
	if req.State != "" {
		qq.Set("state", req.State)
	}
	dest.RawQuery = qq.Encode()
	writeJSON(w, http.StatusOK, map[string]string{"redirectTo": dest.String()})
}
```

---

### Task 8: `POST /oauth/token` (code + refresh grants)

**Files:**
- Modify: `apps/api-go/handlers_oauth_provider.go`

- [ ] **Step 1: Write the implementation**

Adicionar ao final de `apps/api-go/handlers_oauth_provider.go`:

```go
// POST /oauth/token — application/x-www-form-urlencoded (padrão OAuth).
// Tokens voltam no CORPO (sem cookies): quem gerencia é o app cliente.
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		s.oauthTokenCode(w, r)
	case "refresh_token":
		s.oauthTokenRefresh(w, r)
	default:
		writeErr(w, appErr(http.StatusBadRequest, "UNSUPPORTED_GRANT_TYPE", "grant_type deve ser authorization_code ou refresh_token"))
	}
}

func (s *Server) oauthTokenCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostFormValue("code")
	clientID := r.PostFormValue("client_id")
	redirectURI := r.PostFormValue("redirect_uri")
	verifier := r.PostFormValue("code_verifier")
	if code == "" || clientID == "" || redirectURI == "" || verifier == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "code, client_id, redirect_uri e code_verifier são obrigatórios"))
		return
	}
	// GETDEL: o code é de uso único — a segunda troca falha sempre.
	raw, err := s.rdb.GetDel(r.Context(), authCodeKey(code)).Bytes()
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_GRANT", "code inválido ou expirado"))
		return
	}
	var ac authCode
	if err := json.Unmarshal(raw, &ac); err != nil {
		writeErr(w, err)
		return
	}
	if ac.ClientID != clientID || ac.RedirectURI != redirectURI || !verifyPKCE(verifier, ac.CodeChallenge) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_GRANT", "Parâmetros não conferem com o code"))
		return
	}
	u, err := s.userByID(r.Context(), ac.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta indisponível"))
		return
	}
	s.writeTokenResponse(w, r, u)
}

func (s *Server) oauthTokenRefresh(w http.ResponseWriter, r *http.Request) {
	refresh := r.PostFormValue("refresh_token")
	if refresh == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "refresh_token é obrigatório"))
		return
	}
	if _, err := verifyToken(refresh, s.cfg.JWTRefreshSecret); err != nil {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_GRANT", "refresh_token inválido"))
		return
	}
	sid, uid, expires, err := s.sessionByHash(r.Context(), hashRefreshToken(refresh))
	if err != nil || expires.Before(time.Now()) {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_GRANT", "Sessão expirada"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta indisponível"))
		return
	}
	_ = s.deleteSession(r.Context(), sid) // rotaciona, como /auth/refresh
	s.writeTokenResponse(w, r, u)
}

// writeTokenResponse emite tokens + sessão e responde no formato OAuth.
func (s *Server) writeTokenResponse(w http.ResponseWriter, r *http.Request, u *User) {
	access, refresh, err := generateTokens(s.cfg.JWTSecret, s.cfg.JWTRefreshSecret, u.ID, u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.createSession(r.Context(), u.ID, hashRefreshToken(refresh), time.Now().Add(refreshTTL)); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "refresh_token": refresh,
		"token_type": "Bearer", "expires_in": int(accessTTL.Seconds()),
	})
}
```

- [ ] **Step 2: Run ALL Task 6–8 tests**

Run: `cd apps/api-go && go test -run TestOAuth ./... && gofmt -l . && go vet ./... && go build ./...`
Expected: PASS, gofmt vazio, vet limpo

- [ ] **Step 3: Commit (Tasks 6–8 juntas)**

```bash
git add apps/api-go/handlers_oauth_provider.go apps/api-go/handlers_oauth_provider_test.go apps/api-go/routes.go
git commit -m "feat(api-go): fluxo OAuth provider — authorize, confirm e token (PKCE S256)"
```

---

### Task 9: Admin CRUD de `oauth_clients`

**Files:**
- Create: `apps/api-go/handlers_admin_clients.go`
- Test: `apps/api-go/handlers_admin_clients_test.go`
- Modify: `apps/api-go/routes.go`

- [ ] **Step 1: Write the failing test** (validação pura)

Criar `apps/api-go/handlers_admin_clients_test.go`:

```go
package main

import "testing"

func TestValidateOAuthClientInput(t *testing.T) {
	cases := []struct {
		clientID string
		uris     []string
		ok       bool
	}{
		{"meu-app", []string{"https://app.santos-tech.com/callback"}, true},
		{"MeuApp_2", []string{"https://x.com/cb"}, true},
		{"", []string{"https://x.com/cb"}, false},              // client_id vazio
		{"tem espaço", []string{"https://x.com/cb"}, false},    // chars inválidos
		{"ok", nil, false},                                     // sem redirect
		{"ok", []string{"notaurl"}, false},                     // uri inválida
		{"ok", []string{"https://x.com/cb", ""}, false},        // uri vazia
	}
	for _, c := range cases {
		err := validateOAuthClientInput(c.clientID, c.uris)
		if (err == nil) != c.ok {
			t.Errorf("clientID=%q uris=%v: err=%v, esperava ok=%v", c.clientID, c.uris, err, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/api-go && go test -run TestValidateOAuthClient ./...`
Expected: FAIL (compile error)

- [ ] **Step 3: Write the implementation**

Criar `apps/api-go/handlers_admin_clients.go`:

```go
package main

import (
	"net/http"
	"net/url"
	"regexp"
)

// Gestão admin das aplicações OAuth ("Entrar com Santos Tech").
// Padrão: handlers_admin_users.go (adminGuard nas rotas).

var clientIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validateOAuthClientInput(clientID string, uris []string) error {
	if !clientIDRe.MatchString(clientID) {
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "client_id deve ter 1-64 chars alfanuméricos, _ ou -")
	}
	if len(uris) == 0 {
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "informe ao menos um redirect_uri")
	}
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "redirect_uri inválido: "+raw)
		}
	}
	return nil
}

// GET /auth/admin/oauth-clients
func (s *Server) handleListOAuthClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.listOAuthClients(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": clients})
}

// POST /auth/admin/oauth-clients {clientId, name, redirectUris}
func (s *Server) handleCreateOAuthClient(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID     string   `json:"clientId"`
		Name         string   `json:"name"`
		RedirectURIs []string `json:"redirectUris"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "clientId, name e redirectUris são obrigatórios"))
		return
	}
	if err := validateOAuthClientInput(body.ClientID, body.RedirectURIs); err != nil {
		writeErr(w, err)
		return
	}
	if existing, err := s.oauthClientByClientID(r.Context(), body.ClientID); err != nil {
		writeErr(w, err)
		return
	} else if existing != nil {
		writeErr(w, appErr(http.StatusConflict, "CLIENT_ALREADY_EXISTS", "Já existe uma aplicação com este client_id"))
		return
	}
	c, err := s.insertOAuthClient(r.Context(), body.ClientID, body.Name, body.RedirectURIs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// PATCH /auth/admin/oauth-clients/{id} {name?, redirectUris?, isActive?}
func (s *Server) handleUpdateOAuthClient(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         *string  `json:"name"`
		RedirectURIs []string `json:"redirectUris"`
		IsActive     *bool    `json:"isActive"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if body.RedirectURIs != nil {
		if err := validateOAuthClientInput("placeholder", body.RedirectURIs); err != nil {
			writeErr(w, err)
			return
		}
	}
	c, err := s.updateOAuthClient(r.Context(), r.PathValue("id"), body.Name, body.RedirectURIs, body.IsActive)
	if err != nil {
		writeErr(w, err)
		return
	}
	if c == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Aplicação não encontrada"))
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// DELETE /auth/admin/oauth-clients/{id}
func (s *Server) handleDeleteOAuthClient(w http.ResponseWriter, r *http.Request) {
	ok, err := s.deleteOAuthClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Aplicação não encontrada"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Em `apps/api-go/routes.go`, após o bloco de gestão admin de usuários, adicionar:

```go
	// Gestão admin de aplicações OAuth ("Entrar com Santos Tech")
	mux.HandleFunc("GET /auth/admin/oauth-clients", s.adminGuard(s.handleListOAuthClients))
	mux.HandleFunc("POST /auth/admin/oauth-clients", s.rateLimit(10, min, s.adminGuard(s.handleCreateOAuthClient)))
	mux.HandleFunc("PATCH /auth/admin/oauth-clients/{id}", s.rateLimit(20, min, s.adminGuard(s.handleUpdateOAuthClient)))
	mux.HandleFunc("DELETE /auth/admin/oauth-clients/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteOAuthClient)))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd apps/api-go && go test ./... && gofmt -l . && go vet ./... && go build ./...`
Expected: PASS (todos), gofmt vazio

- [ ] **Step 5: Commit**

```bash
git add apps/api-go/handlers_admin_clients.go apps/api-go/handlers_admin_clients_test.go apps/api-go/routes.go
git commit -m "feat(api-go): CRUD admin de aplicações OAuth (oauth_clients)"
```

---

### Task 10: Google OAuth carrega `return_to` (volta ao chooser após login Google)

**Files:**
- Modify: `apps/api-go/handlers_oauth.go`

O chooser manda "Usar outra conta" pra tela de login; se o usuário escolhe Google, o callback hoje SEMPRE volta pra raiz do auth-web — perderia o `request_id`. Solução: `GET /auth/google?return_to=/oauth/choose?request_id=x` guarda o caminho no cookie `oauth_state` e o callback respeita.

- [ ] **Step 1: Write the failing test**

Adicionar a `apps/api-go/handlers_oauth_provider_test.go`:

```go
func TestGoogleStartStoresReturnTo(t *testing.T) {
	s := testServer(Config{GoogleClientID: "x"})
	s.google = &oauth2.Config{ClientID: "x"}
	r := httptest.NewRequest("GET", "/auth/google?return_to=/oauth/choose%3Frequest_id%3Dabc", nil)
	w := httptest.NewRecorder()
	s.handleGoogleStart(w, r)
	var stateCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "oauth_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil || !strings.Contains(stateCookie.Value, "|/oauth/choose?request_id=abc") {
		t.Fatalf("cookie oauth_state deveria carregar o return_to: %+v", stateCookie)
	}
}

func TestGoogleStartRejectsExternalReturnTo(t *testing.T) {
	s := testServer(Config{GoogleClientID: "x"})
	s.google = &oauth2.Config{ClientID: "x"}
	r := httptest.NewRequest("GET", "/auth/google?return_to=//evil.com/x", nil)
	w := httptest.NewRecorder()
	s.handleGoogleStart(w, r)
	for _, c := range w.Result().Cookies() {
		if c.Name == "oauth_state" && strings.Contains(c.Value, "evil.com") {
			t.Fatal("return_to externo não pode entrar no state")
		}
	}
}
```

E adicionar `"golang.org/x/oauth2"` aos imports do arquivo de teste.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/api-go && go test -run TestGoogleStart ./...`
Expected: FAIL (cookie não contém o return_to)

- [ ] **Step 3: Write the implementation**

Em `apps/api-go/handlers_oauth.go`, substituir `handleGoogleStart`:

```go
// GET /auth/google?return_to=/caminho — redireciona pro consentimento do Google.
// return_to (opcional, só caminho relativo) é devolvido pelo callback — usado
// pelo fluxo OAuth provider pra voltar ao chooser com o request_id.
func (s *Server) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		writeErr(w, appErr(http.StatusInternalServerError, "OAUTH_DISABLED", "OAuth não configurado"))
		return
	}
	state := randomToken(16)
	cookieVal := state
	// Só caminho relativo ("/x"), nunca "//host" — evita open-redirect.
	if rt := r.URL.Query().Get("return_to"); strings.HasPrefix(rt, "/") && !strings.HasPrefix(rt, "//") {
		cookieVal = state + "|" + rt
	}
	http.SetCookie(w, &http.Cookie{
		Name: "oauth_state", Value: cookieVal, Path: "/",
		HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	http.Redirect(w, r, s.google.AuthCodeURL(state), http.StatusFound)
}
```

**Atenção:** o valor do cookie agora é `state|return_to`. No `handleGoogleCallback`, ajustar a validação do state e os redirects de sucesso/MFA:

```go
	// valida state (CSRF) — o cookie pode carregar "|return_to" após o state
	sc, err := r.Cookie("oauth_state")
	if err != nil || sc.Value == "" {
		fail("oauth_failed")
		return
	}
	state, returnTo, _ := strings.Cut(sc.Value, "|")
	if state != q.Get("state") {
		fail("oauth_failed")
		return
	}
```

No redirect com MFA (final do bloco `if u.MFAEnabled`), incluir o return_to:

```go
		dest := origin + "/?mfa_challenge=" + challenge + "&mfa_method=" + u.MFAMethod +
			"&mfa_methods=" + strings.Join(mfaMethods(u), ",")
		if returnTo != "" {
			dest += "&return_to=" + url.QueryEscape(returnTo)
		}
		http.Redirect(w, r, dest, http.StatusFound)
		return
```

E no redirect final de sucesso:

```go
	if err := s.issueSession(r.Context(), w, r, u); err != nil {
		fail("oauth_failed")
		return
	}
	dest := origin
	if returnTo != "" {
		dest = origin + returnTo
	}
	http.Redirect(w, r, dest, http.StatusFound)
```

Adicionar `"net/url"` aos imports de `handlers_oauth.go` (caso `url.QueryEscape` ainda não seja usado — o arquivo já importa `net/url`? Conferir; se não, adicionar).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd apps/api-go && go test ./... && gofmt -l . && go vet ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/api-go/handlers_oauth.go apps/api-go/handlers_oauth_provider_test.go
git commit -m "feat(api-go): login Google carrega return_to (volta ao chooser OAuth)"
```

---

### Task 11: auth-web — chooser `/oauth/choose` + login com `request_id`

**Files:**
- Modify: `apps/auth-web/src/lib/auth.ts`
- Create: `apps/auth-web/src/routes/oauth-choose.tsx`
- Modify: `apps/auth-web/src/main.tsx`
- Modify: `apps/auth-web/src/routes/index.tsx`
- Modify: `apps/auth-web/src/components/GoogleButton.tsx`

- [ ] **Step 1: API client das contas**

Em `apps/auth-web/src/lib/auth.ts`, adicionar ao final:

```ts
// ── Multi-conta + OAuth provider ─────────────────────────────────────────────

export type Account = {
  sessionId: string
  name: string
  email: string
  avatarUrl?: string | null
  active: boolean
}

export async function listAccounts(): Promise<{ accounts: Account[] }> {
  return apiFetch<{ accounts: Account[] }>('/auth/accounts')
}

export async function removeAccount(sessionId: string): Promise<void> {
  await apiFetch(`/auth/accounts/${sessionId}`, { method: 'DELETE' })
}

// Confirma a conta escolhida no fluxo OAuth; devolve a URL de retorno pro app.
export async function confirmAuthorize(
  requestId: string,
  sessionId: string,
): Promise<{ redirectTo: string }> {
  return apiFetch<{ redirectTo: string }>('/oauth/authorize/confirm', {
    method: 'POST',
    body: JSON.stringify({ requestId, sessionId }),
  })
}
```

- [ ] **Step 2: Componente do chooser**

Criar `apps/auth-web/src/routes/oauth-choose.tsx`:

```tsx
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { X } from 'lucide-react'
import AuthLayout from '@/components/AuthLayout'
import { listAccounts, removeAccount, confirmAuthorize, type Account } from '@/lib/auth'
import { ApiError } from '@/lib/api'

// Chooser de contas do fluxo OAuth ("Entrar com Santos Tech"), estilo Google:
// lista as contas logadas neste navegador; escolher uma emite o code e volta
// pro app. "Usar outra conta" vai pro login preservando o request_id.
export default function OAuthChoosePage() {
  const requestId = new URLSearchParams(window.location.search).get('request_id')
  const queryClient = useQueryClient()
  const [error, setError] = useState('')
  const [expired, setExpired] = useState(false)

  const { data, isLoading } = useQuery({
    queryKey: ['accounts'],
    queryFn: listAccounts,
    enabled: !!requestId,
  })

  const confirm = useMutation({
    mutationFn: (sessionId: string) => confirmAuthorize(requestId!, sessionId),
    onSuccess: (res) => { window.location.href = res.redirectTo },
    onError: (err) => {
      if (err instanceof ApiError && err.code === 'REQUEST_EXPIRED') {
        setExpired(true)
        return
      }
      if (err instanceof ApiError && err.code === 'SESSION_EXPIRED') {
        // sessão morreu entre o render e o clique: recarrega a lista (já podada)
        queryClient.invalidateQueries({ queryKey: ['accounts'] })
        setError('Essa sessão expirou. Escolha outra conta ou entre novamente.')
        return
      }
      setError(err instanceof ApiError ? err.message : 'Erro ao continuar')
    },
  })

  const remove = useMutation({
    mutationFn: removeAccount,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['accounts'] }),
  })

  const loginUrl = `/?request_id=${encodeURIComponent(requestId ?? '')}`

  if (!requestId || expired) {
    return (
      <AuthLayout>
        <h2 className="text-3xl font-bold text-[#0E2937] mb-1">Sessão de autorização expirou</h2>
        <p className="text-base text-[#496B84] mb-8">
          Volte ao aplicativo de origem e tente entrar novamente.
        </p>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <h2 className="text-3xl font-bold text-[#0E2937] mb-1">Escolha uma conta</h2>
      <p className="text-base text-[#496B84] mb-8">para continuar no aplicativo</p>

      {error && (
        <div className="mb-5 p-4 bg-red-50 border border-red-200 rounded-xl text-sm text-red-700">
          {error}
        </div>
      )}

      {isLoading ? (
        <p className="text-sm text-[#496B84]">Carregando contas...</p>
      ) : (
        <div className="flex flex-col gap-3">
          {(data?.accounts ?? []).map((account: Account) => (
            <div
              key={account.sessionId}
              className="group flex items-center gap-3 p-3 border border-gray-200 rounded-xl bg-white hover:bg-[#F5F8FA] hover:border-[#187ABF] transition-colors"
            >
              <button
                type="button"
                disabled={confirm.isPending}
                onClick={() => { setError(''); confirm.mutate(account.sessionId) }}
                className="flex flex-1 items-center gap-3 text-left disabled:opacity-60"
              >
                {account.avatarUrl ? (
                  <img src={account.avatarUrl} alt="" className="w-10 h-10 rounded-full object-cover" />
                ) : (
                  <div className="w-10 h-10 rounded-full bg-[#187ABF] text-white flex items-center justify-center font-semibold">
                    {account.name.charAt(0).toUpperCase()}
                  </div>
                )}
                <div className="min-w-0">
                  <p className="font-semibold text-[#0E2937] truncate">{account.name}</p>
                  <p className="text-sm text-[#496B84] truncate">{account.email}</p>
                </div>
              </button>
              <button
                type="button"
                aria-label={`Remover ${account.email}`}
                onClick={() => remove.mutate(account.sessionId)}
                className="p-1.5 rounded-lg text-gray-400 hover:text-red-600 hover:bg-red-50 opacity-0 group-hover:opacity-100 transition-opacity"
              >
                <X size={16} />
              </button>
            </div>
          ))}

          <a
            href={loginUrl}
            className="flex items-center justify-center gap-2 p-3 border border-dashed border-gray-300 rounded-xl text-sm font-medium text-[#187ABF] hover:bg-[#F5F8FA] transition-colors"
          >
            Usar outra conta
          </a>
        </div>
      )}
    </AuthLayout>
  )
}
```

- [ ] **Step 3: Registrar a rota**

Em `apps/auth-web/src/main.tsx`:

```tsx
import OAuthChoosePage from '@/routes/oauth-choose'
```

```tsx
const oauthChooseRoute = createRoute({ getParentRoute: () => rootRoute, path: '/oauth/choose', component: OAuthChoosePage })
```

E no `addChildren`:

```tsx
  routeTree: rootRoute.addChildren([loginRoute, forgotRoute, resetRoute, confirmRoute, oauthChooseRoute]),
```

- [ ] **Step 4: Login volta ao chooser (senha, MFA e Google)**

Em `apps/auth-web/src/routes/index.tsx`:

1. Após a linha do `rawRedirect` (linha ~17), adicionar:

```tsx
  // Fluxo OAuth provider: chegamos aqui via "Usar outra conta" do chooser.
  // Após logar, voltamos ao chooser com o mesmo request_id.
  const requestId = new URLSearchParams(window.location.search).get('request_id')
  // Login Google com MFA: o callback devolve return_to junto do desafio.
  const mfaReturnTo = new URLSearchParams(window.location.search).get('return_to')
```

2. Substituir `finishLogin`:

```tsx
  function finishLogin() {
    if (requestId) {
      window.location.href = `/oauth/choose?request_id=${encodeURIComponent(requestId)}`
      return
    }
    if (mfaReturnTo && mfaReturnTo.startsWith('/') && !mfaReturnTo.startsWith('//')) {
      window.location.href = mfaReturnTo
      return
    }
    window.location.href = getSafeRedirect(rawRedirect)
  }
```

3. No `useQuery` do `me` (sessão já ativa ao abrir a tela), trocar o `select` para respeitar o fluxo OAuth — quem chega com `request_id` quer ADICIONAR conta, não ser redirecionado:

```tsx
  useQuery({
    queryKey: ['me'],
    queryFn: me,
    retry: false,
    enabled: !requestId, // adicionando conta: deixa logar de novo
    select: (data) => {
      if (data?.user) window.location.href = getSafeRedirect(rawRedirect)
      return data
    },
  })
```

4. Trocar `<GoogleButton />` por `<GoogleButton returnTo={requestId ? `/oauth/choose?request_id=${requestId}` : undefined} />`.

- [ ] **Step 5: GoogleButton com return_to**

Em `apps/auth-web/src/components/GoogleButton.tsx`, substituir o componente:

```tsx
export default function GoogleButton({ returnTo }: { returnTo?: string }) {
  const base = `${import.meta.env.VITE_API_URL ?? ''}/auth/google`
  const href = returnTo ? `${base}?return_to=${encodeURIComponent(returnTo)}` : base
  return (
    <button
      type="button"
      onClick={() => { window.location.href = href }}
      className="w-full flex items-center justify-center gap-3 px-4 py-2.5 border border-gray-200 rounded-lg text-sm font-medium text-gray-700 bg-white hover:bg-gray-50 transition-colors"
    >
```

(o SVG interno permanece igual)

- [ ] **Step 6: Build (type-check)**

Run: `cd apps/auth-web && bun run build`
Expected: sem erros de tipo, build OK

- [ ] **Step 7: Commit**

```bash
git add apps/auth-web/src
git commit -m "feat(auth-web): chooser de contas /oauth/choose e login com request_id"
```

---

### Task 12: Documentação — `openapi.yaml` + `llms.txt`

**Files:**
- Modify: `docs/openapi.yaml`
- Modify: `apps/api-go/llms.txt`

- [ ] **Step 1: openapi.yaml**

Adicionar as rotas novas seguindo o estilo existente do arquivo (ler antes para casar a estrutura de `paths`, `components` e exemplos de erro):

- `GET /auth/accounts` → 200 `{accounts: [{sessionId, name, email, avatarUrl, active}]}`
- `DELETE /auth/accounts/{sessionId}` → 204 | 404 `ACCOUNT_NOT_FOUND`
- `POST /auth/accounts/{sessionId}/activate` → 200 `{user}` | 401 `SESSION_EXPIRED` | 403 `ACCOUNT_SUSPENDED` (rate 20/min)
- `GET /oauth/authorize` (query: client_id, redirect_uri, response_type=code, state?, code_challenge, code_challenge_method=S256) → 302 para o chooser | 400 `VALIDATION_ERROR`/`INVALID_CLIENT` | 302 `?error=` (rate 30/min)
- `POST /oauth/authorize/confirm` `{requestId, sessionId}` → 200 `{redirectTo}` | 410 `REQUEST_EXPIRED` | 401 `SESSION_EXPIRED` (rate 20/min)
- `POST /oauth/token` (form: grant_type, code?, client_id?, redirect_uri?, code_verifier?, refresh_token?) → 200 `{access_token, refresh_token, token_type, expires_in}` | 400 `INVALID_GRANT`/`UNSUPPORTED_GRANT_TYPE` (rate 20/min)
- `GET/POST/PATCH/DELETE /auth/admin/oauth-clients[/{id}]` → CRUD admin (espelhar o formato de `/auth/admin/users`)
- `GET /auth/google`: documentar o novo query param `return_to`

- [ ] **Step 2: llms.txt**

Em `apps/api-go/llms.txt`, na seção da Auth API, adicionar resumo das rotas novas no mesmo formato das existentes (tabela rota/método/auth), incluindo: o fluxo OAuth provider em 3 passos (authorize → choose/confirm → token), PKCE S256 obrigatório, codes de uso único com TTL 60s, e os endpoints de multi-conta.

- [ ] **Step 3: Commit**

```bash
git add docs/openapi.yaml apps/api-go/llms.txt
git commit -m "docs: OAuth provider e multi-conta no openapi.yaml e llms.txt"
```

---

### Task 13: Verificação integrada (Postgres + Redis reais)

**Files:** nenhum novo — validação ponta a ponta.

- [ ] **Step 1: Subir os bancos e rodar a suíte completa**

```bash
docker compose -f infra/docker-compose.yml up -d postgres redis
cd apps/api-go && go test ./...
```
Expected: PASS (incluindo blocos de integração)

- [ ] **Step 2: Smoke test manual do fluxo OAuth**

Com a API rodando local (`cd apps/api-go && go run .`) e um client criado via SQL direto:

```bash
docker compose -f infra/docker-compose.yml exec postgres psql -U postgres -c \
  "INSERT INTO oauth_clients (client_id, name, redirect_uris) VALUES ('teste', 'App Teste', ARRAY['http://localhost:9999/cb']) ON CONFLICT (client_id) DO NOTHING;"
```

1. Logar duas contas no auth-web local (`cd apps/auth-web && bun run dev`) — confirmar que o cookie `accounts` tem 2 ids (`GET /auth/accounts` devolve 2 contas).
2. Gerar PKCE e abrir o authorize:

```bash
VERIFIER=$(openssl rand -hex 32)
CHALLENGE=$(printf %s "$VERIFIER" | openssl dgst -sha256 -binary | basenc --base64url | tr -d '=')
echo "http://localhost:3333/oauth/authorize?client_id=teste&redirect_uri=http://localhost:9999/cb&response_type=code&state=xyz&code_challenge=$CHALLENGE&code_challenge_method=S256"
```

3. No navegador: chooser aparece com as 2 contas → escolher uma → captura o `code` da URL de redirect (`http://localhost:9999/cb?code=...&state=xyz`).
4. Trocar o code:

```bash
curl -s -X POST http://localhost:3333/oauth/token \
  -d grant_type=authorization_code -d code=<CODE> -d client_id=teste \
  -d redirect_uri=http://localhost:9999/cb -d code_verifier=$VERIFIER
```
Expected: `{access_token, refresh_token, token_type: "Bearer", expires_in: 7200}`

5. Repetir o passo 4 com o MESMO code → `INVALID_GRANT` (uso único).
6. `curl -s http://localhost:3333/auth/me -H "Authorization: Bearer <ACCESS>"` → perfil da conta escolhida.
7. Logout de uma conta no auth-web → `GET /auth/accounts` devolve só a outra.

- [ ] **Step 3: Checklist pré-commit final**

```bash
cd apps/api-go && gofmt -l . && go vet ./... && go build ./... && go test ./...
cd ../auth-web && bun run build
git diff && git diff --staged
```

Rodar `/code-review` e `/security-review` (mexemos em auth, JWT, cookies e sessão — obrigatório pelo CLAUDE.md).

- [ ] **Step 4: Commit final (se houver ajustes da revisão)**

```bash
git add -A && git commit -m "fix(api-go): ajustes da revisão do fluxo OAuth provider"
```
