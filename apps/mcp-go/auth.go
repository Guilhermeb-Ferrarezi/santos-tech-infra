package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Papel Admin no auth central (api-go, models.go: RoleAdmin = 3).
const roleAdmin = 3

// Headers internos usados para levar a identidade validada pelo requireAuth até
// os handlers de tool (o SDK MCP propaga http.Header em RequestExtra.Header).
// São SEMPRE apagados na entrada e reescritos pelo requireAuth, então valor
// vindo do cliente nunca sobrevive.
const (
	headerAuthRole  = "X-Mcp-Auth-Role"
	headerAuthEmail = "X-Mcp-Auth-Email"
)

// identity é o resultado da validação do token no auth central.
type identity struct {
	Role  int
	Email string
}

// TTLs do cache de validação. Curtos de propósito: o objetivo é evitar um
// round-trip por chamada de tool, não guardar sessão. O cache negativo corta
// amplificação de token inválido repetido.
const (
	authCacheTTL    = 30 * time.Second
	authCacheNegTTL = 10 * time.Second
	authCacheMax    = 5000
)

type authCacheEntry struct {
	id      identity
	ok      bool
	expires time.Time
}

type authCache struct {
	mu sync.Mutex
	m  map[string]authCacheEntry
}

func newAuthCache() *authCache { return &authCache{m: make(map[string]authCacheEntry)} }

func authCacheKey(authorization string) string {
	sum := sha256.Sum256([]byte(authorization))
	return hex.EncodeToString(sum[:])
}

func (c *authCache) get(key string) (authCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Now().After(e.expires) {
		return authCacheEntry{}, false
	}
	return e, true
}

func (c *authCache) put(key string, e authCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Teto simples de memória: estourou, zera tudo (o custo é revalidar).
	if len(c.m) >= authCacheMax {
		c.m = make(map[string]authCacheEntry, authCacheMax)
	}
	c.m[key] = e
}

// authMeURL devolve a URL do /auth/me do auth central. Vazia = não há downstream
// configurado (só acontece em teste/dev sem AUTH_API_URL): nesse caso o
// requireAuth não consegue validar e o gateway roda em modo degradado — sem
// identidade, as tools que usam credencial de serviço ficam bloqueadas.
func (s *Server) authMeURL() string {
	if s.cfg.AuthMeURL != "" {
		return s.cfg.AuthMeURL
	}
	if s.cfg.AuthBaseURL != "" {
		return s.cfg.AuthBaseURL + "/auth/me"
	}
	return ""
}

// verifyToken valida o Authorization no auth central (GET /auth/me) e devolve a
// identidade (papel + email). Fail-closed: qualquer erro de rede, timeout,
// status != 200 ou corpo indecifrável vira "não autenticado".
func (s *Server) verifyToken(ctx context.Context, authorization string) (identity, bool) {
	meURL := s.authMeURL()
	if meURL == "" {
		return identity{}, false
	}
	key := authCacheKey(authorization)
	if e, hit := s.authCache.get(key); hit {
		return e.id, e.ok
	}

	id, ok := s.callAuthMe(ctx, meURL, authorization)
	ttl := authCacheNegTTL
	if ok {
		ttl = authCacheTTL
	}
	s.authCache.put(key, authCacheEntry{id: id, ok: ok, expires: time.Now().Add(ttl)})
	return id, ok
}

func (s *Server) callAuthMe(ctx context.Context, meURL, authorization string) (identity, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meURL, nil)
	if err != nil {
		return identity{}, false
	}
	req.Header.Set("Authorization", authorization)
	resp, err := s.authClient.Do(req)
	if err != nil {
		return identity{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return identity{}, false
	}
	var me struct {
		User struct {
			Role  int    `json:"role"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&me); err != nil {
		return identity{}, false
	}
	return identity{Role: me.User.Role, Email: me.User.Email}, true
}

// identityFromHeader lê a identidade que o requireAuth carimbou no request.
func identityFromHeader(h http.Header) (identity, bool) {
	if h == nil {
		return identity{}, false
	}
	raw := h.Get(headerAuthRole)
	if raw == "" {
		return identity{}, false
	}
	role, err := strconv.Atoi(raw)
	if err != nil {
		return identity{}, false
	}
	return identity{Role: role, Email: h.Get(headerAuthEmail)}, true
}
