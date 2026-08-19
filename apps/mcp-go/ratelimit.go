package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Rate limit do gateway MCP.
//
// Por que precisa: antes daqui não havia limitador nenhum — só o ipBanCheck,
// que é fail-open — e o cliente HTTP (client.go) faz até 3 tentativas em erro
// transiente. Cada request que chegasse virava até 3 chamadas contra as APIs
// internas: um único cliente amplificava 3x em cima do api-go, do bot-go e do
// payments-go.
//
// Namespace "mcp-go:rl:" conforme a exigência do CLAUDE.md de prefixar as
// chaves de Redis por serviço.
const (
	rlPrefix = "mcp-go:rl:"
	// Janela e teto por identidade. O MCP é conversacional: um cliente
	// (Claude Code, claude.ai) dispara rajadas curtas de tool calls, então a
	// janela é de 1 minuto com folga.
	rlWindow = time.Minute
	rlMax    = 120
)

// rateLimitKey identifica quem está chamando: preferencialmente o token (hash,
// nunca o valor), porque é o que amarra o abuso a uma credencial mesmo quando o
// IP muda; sem token utilizável, cai no IP.
func rateLimitKey(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		sum := sha256.Sum256([]byte(auth))
		return rlPrefix + "tok:" + hex.EncodeToString(sum[:8])
	}
	return rlPrefix + "ip:" + clientIP(r)
}

// allow incrementa o contador da janela e diz se ainda está dentro do teto.
// Fail-open quando o Redis não está configurado ou falha: o gateway não pode
// virar indisponível por causa de um limitador auxiliar (o ipBanCheck deste
// serviço já segue a mesma política).
func (s *Server) allow(ctx context.Context, key string) bool {
	if s.rdb == nil {
		return true
	}
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return true
	}
	// ExpireNX: põe o TTL só se a chave ainda não tem um — comando atômico,
	// sem corrida entre INCR e EXPIRE.
	if err := s.rdb.ExpireNX(ctx, key, rlWindow).Err(); err != nil {
		slog.Warn("rate limit: ExpireNX falhou; chave pode não expirar", "err", err)
	}
	return n <= rlMax
}

// rateLimit é o middleware aplicado ao endpoint MCP. Endpoints operacionais
// (/health, /ready, /metrics, metadata OAuth) não passam por aqui — ver Handler.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allow(r.Context(), rateLimitKey(r)) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(rlWindow.Seconds())))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"RATE_LIMITED","message":"Muitas chamadas. Tente de novo em instantes."}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
