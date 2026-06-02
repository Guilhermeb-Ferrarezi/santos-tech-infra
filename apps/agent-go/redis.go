package main

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

func newRedis(url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	c := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return c, nil
}

// ── Lock de turno por conversa (1 turno ativo por vez) ───────────────────────

func lockKey(convID string) string  { return "claude:lock:" + convID }
func stateKey(convID string) string { return "claude:state:" + convID }

// acquireTurn tenta marcar a conversa como ocupada. Retorna true se conseguiu.
func (s *Server) acquireTurn(ctx context.Context, convID string) (bool, error) {
	return s.rdb.SetNX(ctx, lockKey(convID), "1", 30*time.Minute).Result()
}

func (s *Server) releaseTurn(ctx context.Context, convID string) {
	s.rdb.Del(ctx, lockKey(convID))
}

func (s *Server) setState(ctx context.Context, convID, status string) {
	s.rdb.Set(ctx, stateKey(convID), status, time.Hour)
}
