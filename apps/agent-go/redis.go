package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
// Quando rdb é nil (somente em teste), considera o lock adquirido sem I/O.
func (s *Server) acquireTurn(ctx context.Context, convID string) (bool, error) {
	if s.rdb == nil {
		return true, nil
	}
	return s.rdb.SetNX(ctx, lockKey(convID), "1", 30*time.Minute).Result()
}

func (s *Server) releaseTurn(ctx context.Context, convID string) {
	if s.rdb == nil {
		return
	}
	s.rdb.Del(ctx, lockKey(convID))
}

func (s *Server) setState(ctx context.Context, convID, status string) {
	if s.rdb == nil {
		slog.Warn("setState: rdb nil (esperado só em teste; em produção indica misconfiguração)")
		return
	}
	s.rdb.Set(ctx, stateKey(convID), status, time.Hour)
}

// cleanupOrphans limpa estado órfão deixado por um deploy/restart anterior (#4):
// quando o processo morre no meio de um turno, o processo `claude` cai junto mas o
// lock de turno (Redis) e o status 'running' (Postgres + Redis) ficam pendurados —
// travando a conversa como "ocupada" para sempre. Roda no boot, ANTES de servir.
//
// É conservador: só mexe em locks/estados de turno e no status 'running'; não toca
// em mensagens, sessões nem credenciais. Erros não são fatais para o boot.
func cleanupOrphans(ctx context.Context, db *pgxpool.Pool, rdb *redis.Client) error {
	// Postgres: nenhum turno pode estar 'running' logo após o boot (os processos
	// morreram). Volta para 'idle'.
	if _, err := db.Exec(ctx,
		`UPDATE claude_conversations SET status=$1, updated_at=now() WHERE status=$2`,
		StatusIdle, StatusRunning); err != nil {
		return err
	}

	// Redis: apaga locks de turno (claude:lock:*) e estados (claude:state:*) órfãos.
	// SCAN em lotes para não bloquear o Redis com um KEYS em base grande.
	for _, pattern := range []string{"claude:lock:*", "claude:state:*"} {
		var cursor uint64
		for {
			keys, next, err := rdb.Scan(ctx, cursor, pattern, 200).Result()
			if err != nil {
				return err
			}
			if len(keys) > 0 {
				if err := rdb.Del(ctx, keys...).Err(); err != nil {
					return err
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	return nil
}
