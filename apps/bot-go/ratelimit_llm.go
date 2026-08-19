package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// ─────────────────────────────────────────────────────────────────────────────
// LLMRateLimiter — teto no caminho de ENTRADA das mensagens.
//
// Cada mensagem recebida dispara um agentGoRequest{Model:"opus", Web:true}, e
// nada limitava quantos números distintos falavam por minuto: um remetente com
// script (ou muitos números) transformava o webhook num gerador de custo.
//
// Três limites, todos no Redis (valem entre réplicas):
//  1. token bucket por telefone   — contém o flood de um número só;
//  2. token bucket global         — contém o flood distribuído entre números;
//  3. contador diário de chamadas — teto duro de gasto no dia.
//
// Fail-open: se o Redis sumir, as mensagens passam (com log de erro). Derrubar
// o atendimento inteiro por um blip de Redis é pior que a exposição de custo de
// alguns minutos — mas é uma escolha, não um descuido.
// ─────────────────────────────────────────────────────────────────────────────

// tokenBucketScript implementa um token bucket atômico.
// KEYS[1] = chave do bucket
// ARGV[1] = capacidade (burst) | ARGV[2] = tokens por segundo
// ARGV[3] = agora em ms       | ARGV[4] = TTL em segundos
// Retorna 1 se consumiu um token, 0 se estourou.
var tokenBucketScript = redis.NewScript(`
local dados = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local capacidade = tonumber(ARGV[1])
local taxa = tonumber(ARGV[2])
local agora = tonumber(ARGV[3])
local tokens = tonumber(dados[1])
local ts = tonumber(dados[2])
if tokens == nil or ts == nil then
  tokens = capacidade
  ts = agora
end
local decorrido = agora - ts
if decorrido < 0 then decorrido = 0 end
tokens = math.min(capacidade, tokens + (decorrido / 1000.0) * taxa)
local permitido = 0
if tokens >= 1 then
  tokens = tokens - 1
  permitido = 1
end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', agora)
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[4]))
return permitido
`)

type LLMRateLimiter struct {
	rdb    *redis.Client
	logger *slog.Logger

	enabled bool

	phoneBurst  int
	phonePerMin int

	globalBurst  int
	globalPerMin int

	dailyMax int
}

func NewLLMRateLimiter(cfg Config, rdb *redis.Client, logger *slog.Logger) *LLMRateLimiter {
	return &LLMRateLimiter{
		rdb:          rdb,
		logger:       logger,
		enabled:      cfg.RateLimitEnabled,
		phoneBurst:   cfg.RatePhoneBurst,
		phonePerMin:  cfg.RatePhonePerMin,
		globalBurst:  cfg.RateGlobalBurst,
		globalPerMin: cfg.RateGlobalPerMin,
		dailyMax:     cfg.LLMDailyMax,
	}
}

// Allow decide se a mensagem de phone pode seguir para o engine (e portanto
// para o LLM). Devolve o motivo da recusa para log/métrica.
func (rl *LLMRateLimiter) Allow(ctx context.Context, phone string) (bool, string) {
	if rl == nil || !rl.enabled || rl.rdb == nil {
		return true, ""
	}

	if ok := rl.bucket(ctx, "bot:rl:phone:"+phone, rl.phoneBurst, rl.phonePerMin); !ok {
		return false, "phone"
	}
	if ok := rl.bucket(ctx, "bot:rl:global", rl.globalBurst, rl.globalPerMin); !ok {
		return false, "global"
	}
	if ok := rl.daily(ctx); !ok {
		return false, "daily"
	}
	return true, ""
}

// bucket consome um token; fail-open em erro de Redis.
func (rl *LLMRateLimiter) bucket(ctx context.Context, key string, burst, perMin int) bool {
	if burst <= 0 || perMin <= 0 {
		return true
	}
	ttl := 2 * int(time.Minute.Seconds())
	res, err := tokenBucketScript.Run(ctx, rl.rdb,
		[]string{key},
		burst, float64(perMin)/60.0, time.Now().UnixMilli(), ttl,
	).Int()
	if err != nil {
		rl.logger.Error("ratelimit: token bucket indisponível, liberando (fail-open)", "key", key, "err", err)
		return true
	}
	return res == 1
}

// daily aplica o teto diário de chamadas ao LLM; fail-open em erro de Redis.
func (rl *LLMRateLimiter) daily(ctx context.Context) bool {
	if rl.dailyMax <= 0 {
		return true
	}
	key := "bot:rl:llm-daily:" + time.Now().UTC().Format("2006-01-02")
	n, err := rl.rdb.Incr(ctx, key).Result()
	if err != nil {
		rl.logger.Error("ratelimit: contador diário indisponível, liberando (fail-open)", "err", err)
		return true
	}
	if n == 1 {
		// Só na criação; 48h cobre fuso e depuração no dia seguinte.
		if err := rl.rdb.Expire(ctx, key, 48*time.Hour).Err(); err != nil {
			rl.logger.Warn("ratelimit: falha ao expirar contador diário", "err", err)
		}
	}
	return int(n) <= rl.dailyMax
}

// allowInbound é o ponto de uso no servidor. Não há isenção por número: os
// defaults por telefone são folgados o bastante para nunca atrapalhar uma
// pessoa (inclusive o admin) e ainda assim cortar flood automatizado — checar
// a lista de admins aqui custaria uma query ao tenant_config por mensagem.
func (s *Server) allowInbound(ctx context.Context, canal, phone string) bool {
	if s.rateLimit == nil {
		return true
	}
	ok, motivo := s.rateLimit.Allow(ctx, phone)
	if !ok {
		llmRateLimited.WithLabelValues(canal, motivo).Inc()
		s.logger.Warn("ratelimit: mensagem descartada", "canal", canal, "motivo", motivo)
	}
	return ok
}
