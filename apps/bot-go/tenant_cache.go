package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// TenantCache — cache Redis de valores de tenant_config no hot path.
//
// Hoje só cobre debounce_ms, lido a CADA webhook em debounceWindow(). Antes,
// isso era um SELECT no Postgres por mensagem recebida. Agora cacheamos a chave
// "tenant:<id>:debounce_ms" com TTL curto e invalidamos no PATCH /api/config.
//
// FAIL-OPEN: qualquer erro/miss no Redis NUNCA falha a request — o chamador cai
// para o banco. O Redis aqui é otimização, não fonte de verdade.
// ---------------------------------------------------------------------------

// TenantCache encapsula o cache de config por tenant. rdb pode ser nil (Redis
// ausente): todas as operações viram no-op e o chamador usa só o banco.
type TenantCache struct {
	rdb    *redis.Client
	ttl    time.Duration
	logger *slog.Logger
}

// NewTenantCache cria o cache. ttl<=0 cai para 30s.
func NewTenantCache(rdb *redis.Client, ttl time.Duration, logger *slog.Logger) *TenantCache {
	if logger == nil {
		logger = slog.Default()
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &TenantCache{rdb: rdb, ttl: ttl, logger: logger}
}

func (c *TenantCache) enabled() bool { return c != nil && c.rdb != nil }

func debounceCacheKey(tenantID string) string { return "tenant:" + tenantID + ":debounce_ms" }

// GetDebounceMs devolve o debounce_ms cacheado. (ms, hit). hit=false em
// miss/erro (fail-open: o chamador busca no banco e popula com SetDebounceMs).
func (c *TenantCache) GetDebounceMs(ctx context.Context, tenantID string) (int, bool) {
	if !c.enabled() {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	v, err := c.rdb.Get(ctx, debounceCacheKey(tenantID)).Result()
	if err != nil {
		if err != redis.Nil {
			c.logger.Warn("tenantCache: GET debounce_ms falhou (fail-open p/ banco)", "err", err)
		}
		return 0, false
	}
	ms, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return ms, true
}

// SetDebounceMs popula o cache. Fail-open: erro só loga.
func (c *TenantCache) SetDebounceMs(ctx context.Context, tenantID string, ms int) {
	if !c.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	if err := c.rdb.Set(ctx, debounceCacheKey(tenantID), strconv.Itoa(ms), c.ttl).Err(); err != nil {
		c.logger.Warn("tenantCache: SET debounce_ms falhou (segue)", "err", err)
	}
}

// InvalidateDebounce remove a chave do cache (chamado no PATCH de config).
// Fail-open: erro só loga — o TTL curto garante consistência eventual de toda
// forma.
func (c *TenantCache) InvalidateDebounce(ctx context.Context, tenantID string) {
	if !c.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	if err := c.rdb.Del(ctx, debounceCacheKey(tenantID)).Err(); err != nil {
		c.logger.Warn("tenantCache: DEL debounce_ms falhou (TTL cobre)", "err", err)
	}
}
