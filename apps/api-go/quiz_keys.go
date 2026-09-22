package main

// Chaves de acesso para quem usa a extensão do Jev (POST /quiz/answer) sem
// conta santos-tech. Header X-Quiz-Key é uma FORMA ALTERNATIVA de
// autenticação por cima da sessão de sempre (authGuard) — nunca substitui
// nem altera o caminho de sessão, ver quizAccessGuard.
//
// Criação/listagem/revogação das chaves são só psql (sem rota admin — ver
// SQL pronto no report da spec 2026-09-22-extensao-quiz-jev). Este arquivo só
// cuida do lado de leitura/consumo: validar a chave recebida e reservar cota.

import (
	"context"
	"errors"
	"log/slog"
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

// quizNextResetAt: o instante em que o contador desta chave reseta (meia-noite
// UTC seguinte a `now`) — a LÓGICA de reset continua em UTC, sem mudança
// nenhuma (é o que a query em db/query/quiz_keys.sql compara). Só a
// mensagem ao usuário (quizQuotaExceededErr) exibe isso convertido.
func quizNextResetAt(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
}

// quizQuotaExceededErr: a mensagem mostra o reset em horário de Brasília, não
// UTC — pro público brasileiro da extensão (a maioria de quem usa
// X-Quiz-Key), "00:00 UTC" soa arbitrário; 21:00 é a hora real, no relógio de
// quem está lendo, em que a cota volta a valer. portalBRLocation
// (portal_store.go) é FixedZone, não LoadLocation: sem horário de verão no
// Brasil desde 2019, e a imagem final é distroless (sem tzdata) — mesma
// decisão já tomada ali, reaproveitada aqui em vez de duplicar. A lógica de
// reset em si continua inteiramente em UTC (quizNextResetAt) — só a EXIBIÇÃO
// muda.
func quizQuotaExceededErr(now time.Time) *AppError {
	resetAt := quizNextResetAt(now).In(portalBRLocation)
	return appErr(http.StatusTooManyRequests, "QUOTA_EXCEEDED",
		"Limite diário desta chave atingido — tente novamente após "+resetAt.Format("15:04 de 02/01")+" (horário de Brasília)")
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

// quizKeyAuthorize decide só se a chave já carregada do banco EXISTE e está
// utilizável (não revogada) — NÃO decide mais cota. A cota virou reserva
// atômica (quizKeyReserve/reserveQuizKeyUsage), cobrada não aqui no guard mas
// no momento em que a questão já foi validada como respondível — ver o
// comentário grande em quizAccessGuard e a justificativa completa no report
// da correção de corrida (spec 2026-09-22-extensao-quiz-jev/
// task-chaves-fix-report.md): um SELECT aqui e um UPDATE só depois da
// resposta boa deixava a janela inteira da chamada ao modelo (3 a 50s) aberta
// pra N requisições concorrentes lerem o mesmo contador e passarem todas.
//
// Pura (sem I/O) de propósito — é o que dá pra testar sem Postgres neste
// harness (ver quiz_keys_test.go). k == nil representa "chave não encontrada"
// (lookupQuizKey devolve nil, nil nesse caso) — mesmo erro que chave
// revogada, por causa do oráculo citado em quizInvalidKeyErr.
func quizKeyAuthorize(k *quizAccessKey) *AppError {
	if k == nil || k.RevokedAt != nil {
		return quizInvalidKeyErr()
	}
	return nil
}

// quizKeyReserve decide, a partir do estado já carregado da chave, se uma
// reserva de cota pode ser concedida — a MESMA decisão que o UPDATE atômico
// de ReserveQuizKeyUsage aplica em SQL (ver reserveQuizKeyUsage e
// db/query/quiz_keys.sql), reescrita aqui em Go só pra ficar testável sob
// concorrência sem depender de Postgres real — este harness não tem banco
// disponível pros testes Go (ver nota em handlers_social_test.go). A prova de
// que a serialização (uma única reserva concedida quando a cota já está no
// limite, mesmo sob concorrência) se sustenta está em
// TestQuizKeyReserveConcorrenciaSoUmaPassaNoLimite, em quiz_keys_test.go.
func quizKeyReserve(k *quizAccessKey, now time.Time) (nextUsageCount int, granted bool) {
	usage := quizKeyEffectiveUsage(k, now)
	if usage >= k.DailyLimit {
		return usage, false
	}
	return usage + 1, true
}

// lookupQuizKey busca a chave pelo hash SHA-256 do valor recebido no header,
// via sqlc (GetQuizAccessKeyByHash — db/query/quiz_keys.sql). (nil, nil) =
// chave não encontrada — não é erro de banco, quizKeyAuthorize trata igual a
// uma chave revogada.
func (s *Server) lookupQuizKey(ctx context.Context, rawKey string) (*quizAccessKey, error) {
	row, err := s.q.GetQuizAccessKeyByHash(ctx, sha256Hex(rawKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	k := &quizAccessKey{
		ID:         row.ID,
		Label:      row.Label,
		DailyLimit: int(row.DailyLimit),
		UsageDate:  row.UsageDate.Time,
		UsageCount: int(row.UsageCount),
	}
	if row.RevokedAt.Valid {
		t := row.RevokedAt.Time
		k.RevokedAt = &t
	}
	return k, nil
}

// reserveQuizKeyUsage tenta gastar UMA unidade de cota da chave,
// ATOMICAMENTE — um único UPDATE condicional (ReserveQuizKeyUsage, ver
// db/query/quiz_keys.sql) que só afeta a linha se ainda houver cota hoje (e a
// chave não estiver revogada), com RETURNING pra saber se a reserva foi
// concedida. Substitui o antigo par SELECT (no guard, ver quizKeyAuthorize) +
// UPDATE (só depois da resposta boa, ver o antigo incrementQuizKeyUsage): a
// janela entre os dois cobria a chamada inteira ao modelo (3 a 50s) e deixava
// requisições concorrentes da mesma chave lerem o mesmo contador e passarem
// todas. O UPDATE condicional serializa no lock de linha do Postgres — duas
// chamadas concorrentes nunca reservam a mesma vaga porque a segunda só
// enxerga o usage_count já incrementado pela primeira.
//
// Chamada só depois que a questão já foi validada (parseável, imagem ok,
// texto suficiente — ver os pontos de chamada em quiz.go, quizDeps.reserve) e
// ANTES de qualquer chamada ao modelo — corpo inválido (400) e questão não
// separável (422) nunca chegam aqui, então nunca gastam cota. O trade-off
// aceito (ver justificativa completa no report): um upstream que falha
// DEPOIS deste ponto (timeout, sem chave ativa no roteador) já gastou a
// vaga — não há estorno.
func (s *Server) reserveQuizKeyUsage(ctx context.Context, id int64) (bool, error) {
	_, err := s.q.ReserveQuizKeyUsage(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// quizReserveFunc monta o fecho que quiz.go chama (quizDeps.reserve) no
// instante em que a questão já foi validada — nil quando a requisição não
// usa X-Quiz-Key (sessão normal não tem cota de chave pra debitar).
func (s *Server) quizReserveFunc(ctx context.Context, key *quizAccessKey) func() error {
	if key == nil {
		return nil
	}
	return func() error {
		granted, err := s.reserveQuizKeyUsage(ctx, key.ID)
		if err != nil {
			// Falha NOSSA (banco), não do usuário — mesma filosofia que já
			// existia no antigo incrementQuizKeyUsage pós-sucesso (a
			// assimetria documentada no plano): não recusa a resposta por
			// causa disso. A cota fica subcontada nesta falha (a vaga não
			// foi debitada), mas a alternativa — bloquear quem ia pagar uma
			// chamada cara ao modelo por causa de um erro que não é dele —
			// seria pior. Decisão deliberada, não descuido.
			slog.Error("quiz: falha ao reservar cota da chave de acesso — seguindo sem reservar", "quiz_key_label", key.Label, "err", err)
			return nil
		}
		if !granted {
			return quizQuotaExceededErr(time.Now())
		}
		return nil
	}
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
// O guard só AUTENTICA a chave (existe? não está revogada?) — NÃO reserva
// cota aqui. A reserva (atômica, ver reserveQuizKeyUsage) acontece mais
// tarde, dentro de answerQuiz (quiz.go), no instante em que a questão do
// corpo já foi validada como respondível e ANTES de chamar o modelo — porque
// o guard roda ANTES do corpo ser decodificado, não dava pra saber ali se a
// requisição ia mesmo custar uma chamada de LLM. Ver handleQuizAnswer
// (handlers_quiz.go) pra onde a chave carregada aqui (key := ctx.Value) é
// usada de novo.
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
		if qerr := quizKeyAuthorize(key); qerr != nil {
			writeErr(w, qerr)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), quizKeyCtxKey, key)))
	}
}
