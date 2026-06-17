package main

import (
	"context"
	"fmt"
	"time"
)

// chargeStatusTTL é curto de propósito: o cache só evita o re-poll do SSE a cada
// ~20s; a fonte de verdade continua sendo o Postgres e o webhook invalida na hora.
const chargeStatusTTL = 30 * time.Second

func chargeStatusKey(token string) string {
	return fmt.Sprintf("payments-go:charge:%s:status", token)
}

// cacheChargeStatus grava o status atual da cobrança no Redis. Fail-open: erro de
// escrita é ignorado (o cache é apenas otimização).
func (s *Server) cacheChargeStatus(ctx context.Context, token, status string) {
	if s.rdb == nil || token == "" || status == "" {
		return
	}
	_ = s.rdb.Set(ctx, chargeStatusKey(token), status, chargeStatusTTL).Err()
}

// cachedChargeStatus lê o status do cache. Retorna (status, true) em hit. Em miss,
// erro de Redis ou cache desabilitado retorna ("", false) — o caller cai para o banco.
func (s *Server) cachedChargeStatus(ctx context.Context, token string) (string, bool) {
	if s.rdb == nil || token == "" {
		return "", false
	}
	v, err := s.rdb.Get(ctx, chargeStatusKey(token)).Result()
	if err != nil { // redis.Nil (miss) ou indisponível: fail-open para o banco.
		return "", false
	}
	return v, true
}

// invalidateChargeStatus remove (ou reescreve) o cache quando o estado muda. O
// webhook chama isto ao marcar paga/expirada. Fail-open.
func (s *Server) invalidateChargeStatus(ctx context.Context, token string) {
	if s.rdb == nil || token == "" {
		return
	}
	_ = s.rdb.Del(ctx, chargeStatusKey(token)).Err()
}

// chargeStatus resolve o status da cobrança pelo token: tenta o cache Redis e, em
// miss/erro (fail-open), lê do banco e preenche o cache (write-through). Propaga o
// erro do banco (ex.: charge inexistente) para o caller mapear 404.
func (s *Server) chargeStatus(ctx context.Context, token string) (string, error) {
	if status, ok := s.cachedChargeStatus(ctx, token); ok {
		return status, nil
	}
	c, err := s.store.GetChargeByPublicToken(ctx, token)
	if err != nil {
		return "", err
	}
	s.cacheChargeStatus(ctx, token, c.Status)
	return c.Status, nil
}
