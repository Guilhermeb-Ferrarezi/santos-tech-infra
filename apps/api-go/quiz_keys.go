package main

// Chaves de acesso para quem usa a extensão do Jev (POST /quiz/answer) sem
// conta santos-tech. Header X-Quiz-Key é uma FORMA ALTERNATIVA de
// autenticação por cima da sessão de sempre (authGuard) — nunca substitui
// nem altera o caminho de sessão, ver quizAccessGuard.
//
// Criação/listagem/revogação das chaves são só psql (sem rota admin — ver
// SQL pronto no report da spec 2026-09-22-extensao-quiz-jev). Este arquivo só
// cuida do lado de leitura/consumo: validar a chave recebida e contar uso.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// quizKeyPrefix: prefixo reconhecível em log/suporte. Nunca guardamos o valor
// da chave em texto puro — só o hash (ver quiz_access_keys em db.go).
const quizKeyPrefix = "qz_"

// quizAccessKey é a chave já carregada do banco pelo hash. Só o necessário
// pra decidir autorização — o valor real da chave nunca é lido de volta.
type quizAccessKey struct {
	ID         int64
	Label      string
	DailyLimit int
	UsageDate  time.Time
	UsageCount int
	RevokedAt  *time.Time
}

// quizInvalidKeyErr: MESMA mensagem para chave malformada (nem chega a
// consultar o banco) e chave inexistente/revogada (após consultar) — de
// propósito, pra não virar um oráculo que deixa alguém testar prefixos/hashes
// até achar um formato "válido".
func quizInvalidKeyErr() *AppError {
	return appErr(http.StatusUnauthorized, "INVALID_KEY", "Chave de acesso inválida")
}

// quizNextResetAt: a data em que o contador desta chave reseta (meia-noite
// UTC seguinte a `now`) — vai na mensagem de 429 pra quem tomou o limite
// saber quando tentar de novo, sem adivinhar o timezone do servidor.
func quizNextResetAt(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
}

func quizQuotaExceededErr(now time.Time) *AppError {
	resetAt := quizNextResetAt(now)
	return appErr(http.StatusTooManyRequests, "QUOTA_EXCEEDED",
		"Limite diário desta chave atingido — tente novamente após "+resetAt.Format("15:04 de 02/01")+" (UTC)")
}

// quizKeyEffectiveUsage devolve quanto a chave já gastou HOJE (UTC): uso de
// um dia anterior não conta pro limite de hoje — é só comparar usage_date com
// a data corrente, sem agregação nenhuma. Pura (recebe `now`) pra ser
// testável sem depender do relógio real nem do banco.
func quizKeyEffectiveUsage(k *quizAccessKey, now time.Time) int {
	if !sameUTCDate(k.UsageDate, now) {
		return 0
	}
	return k.UsageCount
}

func sameUTCDate(a, b time.Time) bool {
	au, bu := a.UTC(), b.UTC()
	return au.Year() == bu.Year() && au.YearDay() == bu.YearDay()
}

// quizKeyAuthorize decide se a chave já carregada do banco pode seguir.
// Pura (sem I/O) de propósito — é o coração da decisão e o que dá pra testar
// sem Postgres neste harness (ver quiz_keys_test.go). k == nil representa
// "chave não encontrada" (lookupQuizKey devolve nil, nil nesse caso) — mesmo
// erro que chave malformada/revogada, por causa do oráculo citado acima.
func quizKeyAuthorize(k *quizAccessKey, now time.Time) *AppError {
	if k == nil || k.RevokedAt != nil {
		return quizInvalidKeyErr()
	}
	if quizKeyEffectiveUsage(k, now) >= k.DailyLimit {
		return quizQuotaExceededErr(now)
	}
	return nil
}

// lookupQuizKey busca a chave pelo hash SHA-256 do valor recebido no header.
// (nil, nil) = chave não encontrada — não é erro de banco, quizKeyAuthorize
// trata igual a uma chave inválida.
func (s *Server) lookupQuizKey(ctx context.Context, rawKey string) (*quizAccessKey, error) {
	var k quizAccessKey
	err := s.db.QueryRow(ctx, `
		SELECT id, label, daily_limit, usage_date, usage_count, revoked_at
		  FROM quiz_access_keys WHERE key_hash = $1`, sha256Hex(rawKey)).
		Scan(&k.ID, &k.Label, &k.DailyLimit, &k.UsageDate, &k.UsageCount, &k.RevokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &k, nil
}

// incrementQuizKeyUsage soma 1 ao contador do dia corrente (UTC), resetando
// pra 1 se o último uso registrado foi em outro dia — tudo num único UPDATE
// atômico, sem ler-decidir-escrever em passos separados (evitaria corrida
// entre duas requisições concorrentes da mesma chave).
//
// Chamada só depois de uma resposta que REALMENTE consultou o upstream (ver
// handleQuizAnswer): corpo inválido e questão não separável retornam antes
// de chegar aqui, então nunca incrementam — nada foi gasto nesses casos.
func (s *Server) incrementQuizKeyUsage(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `
		UPDATE quiz_access_keys
		   SET usage_count = CASE WHEN usage_date = (now() AT TIME ZONE 'utc')::date
		                          THEN usage_count + 1 ELSE 1 END,
		       usage_date  = (now() AT TIME ZONE 'utc')::date
		 WHERE id = $1`, id)
	return err
}

// quizKeyCtxKeyType: tipo próprio pra não colidir com outras chaves de
// contexto (mesmo padrão de userIDKey em server.go, só que sem precisar de um
// valor sentinela — o tipo já é único).
type quizKeyCtxKeyType struct{}

var quizKeyCtxKey quizKeyCtxKeyType

func quizKeyFromContext(ctx context.Context) *quizAccessKey {
	k, _ := ctx.Value(quizKeyCtxKey).(*quizAccessKey)
	return k
}

// quizAccessGuard aceita DUAS formas de autenticação: a sessão de sempre
// (authGuard, sem nenhuma mudança de comportamento) OU o header X-Quiz-Key —
// pensado pra quem usa a extensão sem conta santos-tech. O header, quando
// presente, tem prioridade: quem manda X-Quiz-Key está se autenticando pela
// chave, não pela sessão do navegador (mesmo que um cookie de sessão también
// esteja presente).
//
// O rate limit de 30/min (rateLimit, aplicado por cima desta guard em
// routes.go) continua valendo pros dois caminhos — não é substituído pelo
// limite diário da chave, que é um teto adicional e mais generoso.
func (s *Server) quizAccessGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawKey := strings.TrimSpace(r.Header.Get("X-Quiz-Key"))
		if rawKey == "" {
			s.authGuard(next)(w, r)
			return
		}
		if !strings.HasPrefix(rawKey, quizKeyPrefix) {
			writeErr(w, quizInvalidKeyErr())
			return
		}
		key, err := s.lookupQuizKey(r.Context(), rawKey)
		if err != nil {
			writeErr(w, err)
			return
		}
		if qerr := quizKeyAuthorize(key, time.Now()); qerr != nil {
			writeErr(w, qerr)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), quizKeyCtxKey, key)))
	}
}
