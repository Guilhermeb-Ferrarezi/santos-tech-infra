package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache-aside com Redis. Regra de ouro: FAIL-OPEN. Um erro/miss no Redis NUNCA
// derruba a request — o helper sempre cai para o fetchFn (banco). O cache é só
// um atalho; a fonte de verdade continua sendo o Postgres.

// Chaves de cache (namespaceadas para não colidir com outros serviços no mesmo
// Redis). Por usuário usamos o id na chave — nunca compartilhamos a entrada de
// um usuário com outro.
const (
	cacheUserPrefix     = "api-go:auth:user:"
	cachePortalOverview = "api-go:portal:overview"
	cacheUserTTL        = 30 * time.Second
	cachePortalTTL      = 5 * time.Minute
	cacheOpTimeout      = 2 * time.Second // teto por operação no Redis (não pendura a request)

	// Arquivos (Google Drive): cada thumbnail/checagem de ancestralidade é uma
	// (ou mais) chamada de rede à API do Drive, repetida a cada item visível
	// na lista/grade e a cada ação (download/rename/delete) — cachear reduz
	// bastante o tráfego contra a cota compartilhada da service account.
	cacheDriveDescendantPrefix = "api-go:drive:descendant:"
	cacheDriveDescendantTTL    = 5 * time.Minute
	cacheDriveThumbnailPrefix  = "api-go:drive:thumb:"
	cacheDriveThumbnailTTL     = time.Hour
)

func cacheUserKey(id int64) string {
	return cacheUserPrefix + strconv.FormatInt(id, 10)
}

// cacheDriveDescendantKey: TTL curto (5min) é uma janela de staleness
// aceitável — o pior caso é continuar aceitando um fileID como "dentro da
// pasta" por até 5min depois dele ter sido movido pra fora dela no Drive real
// (quem chamou já tinha acesso de escrita àquela pasta um instante antes).
func cacheDriveDescendantKey(fileID, rootID string) string {
	return cacheDriveDescendantPrefix + fileID + ":" + rootID
}

func cacheDriveThumbnailKey(fileID string) string {
	return cacheDriveThumbnailPrefix + fileID
}

// getOrSetJSON é o helper genérico de cache-aside: tenta ler `key` do Redis e
// desserializar em T; em hit, devolve o valor cacheado. Em miss/erro de Redis,
// chama fetchFn (banco), e — se o fetch deu certo — grava o resultado em cache
// com TTL (best-effort; erro de escrita só loga). Sempre fail-open.
func getOrSetJSON[T any](ctx context.Context, rdb *redis.Client, key string, ttl time.Duration, fetchFn func(context.Context) (T, error)) (T, error) {
	var zero T
	if rdb != nil {
		rctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
		b, err := rdb.Get(rctx, key).Bytes()
		cancel()
		if err == nil {
			var v T
			if jerr := json.Unmarshal(b, &v); jerr == nil {
				return v, nil
			}
			// JSON corrompido em cache: ignora e segue para o banco.
			slog.Warn("cache: payload inválido, caindo para o banco", "key", key)
		} else if !errors.Is(err, redis.Nil) {
			// Erro de Redis (indisponível etc.): fail-open, vai ao banco.
			slog.Warn("cache: leitura falhou, caindo para o banco", "key", key, "err", err)
		}
	}

	v, err := fetchFn(ctx)
	if err != nil {
		return zero, err
	}

	if rdb != nil {
		if b, jerr := json.Marshal(v); jerr == nil {
			rctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
			if serr := rdb.Set(rctx, key, b, ttl).Err(); serr != nil {
				slog.Warn("cache: escrita falhou (segue)", "key", key, "err", serr)
			}
			cancel()
		}
	}
	return v, nil
}

// cacheDel apaga uma ou mais chaves de cache, best-effort. Erro só loga — uma
// invalidação que falha não pode derrubar a mutação que a disparou (a entrada
// expira pelo TTL de qualquer forma). Usa context.Background com timeout próprio
// para que a invalidação rode mesmo se o ctx da request já estiver sendo cancelado.
func (s *Server) cacheDel(keys ...string) {
	if s.rdb == nil || len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cacheOpTimeout)
	defer cancel()
	if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
		slog.Warn("cache: invalidação falhou (entrada expira pelo TTL)", "keys", keys, "err", err)
	}
}

// invalidateUserCache remove a entrada de cache de um usuário. DEVE ser chamado
// em TODA mutação que altere o usuário (perfil, role, quota, suspensão, MFA,
// senha, avatar, preferências, verificação de email, link OAuth, delete) para
// que o próximo authGuard/permGuard veja o estado novo de imediato (sem esperar
// o TTL). Chave por id — nunca vaza dados entre usuários.
func (s *Server) invalidateUserCache(id int64) {
	if id <= 0 {
		return
	}
	s.cacheDel(cacheUserKey(id))
}

// invalidatePortalOverview remove o agregado do /portal/overview. Chamado em
// create/update/delete de course/module/phase/class/class_rooms (os 5 COUNTs).
func (s *Server) invalidatePortalOverview() {
	s.cacheDel(cachePortalOverview)
}

// cachedUserByID é a versão cacheada de userByID, usada no caminho quente de auth
// (resolveToken → authGuard/adminGuard/permGuard, ~todo request autenticado).
// Cache-aside com TTL curto (30s) e fail-open. Um usuário inexistente (nil) NÃO é
// cacheado, para não fixar um negativo que sobreviveria a uma criação imediata.
func (s *Server) cachedUserByID(ctx context.Context, id int64) (*User, error) {
	if s.rdb == nil || id <= 0 {
		return s.userByID(ctx, id)
	}
	key := cacheUserKey(id)

	rctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
	b, err := s.rdb.Get(rctx, key).Bytes()
	cancel()
	if err == nil {
		var u User
		if jerr := json.Unmarshal(b, &u); jerr == nil {
			return &u, nil
		}
		slog.Warn("cache: usuário com payload inválido, caindo para o banco", "key", key)
	} else if !errors.Is(err, redis.Nil) {
		slog.Warn("cache: leitura de usuário falhou, caindo para o banco", "key", key, "err", err)
	}

	u, err := s.userByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, nil // não cacheia negativo
	}
	if data, jerr := json.Marshal(u); jerr == nil {
		wctx, wcancel := context.WithTimeout(ctx, cacheOpTimeout)
		if serr := s.rdb.Set(wctx, key, data, cacheUserTTL).Err(); serr != nil {
			slog.Warn("cache: escrita de usuário falhou (segue)", "key", key, "err", serr)
		}
		wcancel()
	}
	return u, nil
}
