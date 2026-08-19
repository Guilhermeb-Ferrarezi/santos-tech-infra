package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// adminRole é o valor de UserProfile.Role que representa Admin no auth central
// (api-go/models.go: RoleAdmin = 3).
const adminRole = 3

// sessionCacheMax limita o número de tokens memorizados; ao estourar, o cache
// é zerado (é só cache — a próxima request revalida).
const sessionCacheMax = 1024

type sessionEntry struct {
	ok  bool
	exp time.Time
}

// SessionAuth valida a sessão do auth central repassando os cookies da request
// para /auth/me e exigindo papel Admin. Fail-closed: qualquer erro (URL vazia,
// timeout, status != 200, JSON inválido, papel != Admin) resulta em negação.
//
// O resultado é memorizado por um TTL curto, com chave no hash do token, para
// não bater no auth a cada mensagem que o painel busca.
type SessionAuth struct {
	url    string
	client *http.Client
	ttl    time.Duration

	mu    sync.Mutex
	cache map[string]sessionEntry
}

func NewSessionAuth(url string, timeout, ttl time.Duration) *SessionAuth {
	return &SessionAuth{
		url:    url,
		client: &http.Client{Timeout: timeout},
		ttl:    ttl,
		cache:  make(map[string]sessionEntry),
	}
}

// Authorized devolve true se a request carrega uma sessão de admin válida.
func (a *SessionAuth) Authorized(r *http.Request) bool {
	if a == nil || a.url == "" {
		return false
	}
	tok := sessionToken(r)
	if tok == "" {
		return false
	}
	key := hashToken(tok)
	if ok, hit := a.lookup(key); hit {
		return ok
	}
	ok := a.check(r)
	a.store(key, ok)
	return ok
}

// sessionToken extrai o credencial de sessão da request: cookie access_token
// (o que o painel manda com credentials: include).
func sessionToken(r *http.Request) string {
	if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func (a *SessionAuth) lookup(key string) (ok, hit bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, found := a.cache[key]
	if !found || time.Now().After(e.exp) {
		return false, false
	}
	return e.ok, true
}

func (a *SessionAuth) store(key string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) >= sessionCacheMax {
		a.cache = make(map[string]sessionEntry, sessionCacheMax)
	}
	a.cache[key] = sessionEntry{ok: ok, exp: time.Now().Add(a.ttl)}
}

// check consulta /auth/me repassando os cookies (e o Authorization, se houver).
func (a *SessionAuth) check(r *http.Request) bool {
	ctx, cancel := context.WithTimeout(r.Context(), a.client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Cookie", r.Header.Get("Cookie"))
	if h := r.Header.Get("Authorization"); h != "" {
		req.Header.Set("Authorization", h)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	var me struct {
		User struct {
			Role int `json:"role"`
		} `json:"user"`
	}
	if json.NewDecoder(resp.Body).Decode(&me) != nil {
		return false
	}
	return me.User.Role == adminRole
}
