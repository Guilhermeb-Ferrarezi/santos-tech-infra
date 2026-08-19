package main

import (
	"context"
	"testing"
)

// Sem Redis (dev/local) ou com a flag desligada, o rate limit não pode bloquear
// mensagem nenhuma — é fail-open por contrato.
func TestLLMRateLimiterFailOpen(t *testing.T) {
	cases := []struct {
		nome string
		cfg  Config
	}{
		{"desligado", Config{RateLimitEnabled: false, RatePhoneBurst: 1, RatePhonePerMin: 1}},
		{"ligado sem redis", Config{RateLimitEnabled: true, RatePhoneBurst: 1, RatePhonePerMin: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.nome, func(t *testing.T) {
			rl := NewLLMRateLimiter(tc.cfg, nil, testLogger())
			for i := 0; i < 5; i++ {
				if ok, motivo := rl.Allow(context.Background(), "5516999999999"); !ok {
					t.Fatalf("deveria liberar (fail-open), bloqueou por %q", motivo)
				}
			}
		})
	}

	// Limiter nil (Server sem rate limit configurado) também libera.
	var nilRL *LLMRateLimiter
	if ok, _ := nilRL.Allow(context.Background(), "x"); !ok {
		t.Fatal("limiter nil deveria liberar")
	}
}

func TestServerAllowInboundSemLimiter(t *testing.T) {
	s := &Server{}
	if !s.allowInbound(context.Background(), "whatsapp", "5516999999999") {
		t.Fatal("Server sem rateLimit deveria liberar")
	}
}
