package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// RetryStream — Redis Stream para reprocessamento QUASE em tempo real dos
// webhooks que entraram em retry (status 'failed' por erro de processamento).
//
// Antes, o único caminho de reprocesso era o polling de 60s do worker
// (retryLoop → PendingRetries no Postgres), o que adicionava até 60s de
// latência e carga no DB. Agora, ao marcar um webhook como 'failed', o
// produtor faz XADD neste stream; o consumidor (consumer group) faz XREADGROUP
// e reprocessa imediatamente, dando XACK só após sucesso (at-least-once).
//
// O polling do worker continua existindo como SAFETY NET (rede/stream
// indisponível, mensagens publicadas antes do consumer subir, etc.), mas com
// intervalo maior — ver Config.RetryPollInterval.
//
// Fail-open de CACHE não se aplica aqui (isto é FILA): o enqueue (Publish)
// pode falhar; quando falha, logamos e seguimos — o polling do Postgres é a
// rede de segurança que garante que o evento não se perde (ele já está
// persistido como 'failed' + next_retry_at no banco).
// ---------------------------------------------------------------------------

// RetryStream encapsula publicação e consumo do stream de retries de webhook.
type RetryStream struct {
	rdb      *redis.Client
	stream   string
	group    string
	consumer string
	logger   *slog.Logger
}

// retryStreamField — chave do campo no XADD (o payload é só o ID do webhook).
const retryStreamField = "webhook_id"

// NewRetryStream cria o stream de retries. rdb pode ser nil (ex.: testes ou
// Redis ausente): nesse caso Publish/Consume viram no-ops graciosos e o sistema
// cai inteiramente no polling do Postgres.
func NewRetryStream(rdb *redis.Client, stream, group, consumer string, logger *slog.Logger) *RetryStream {
	if logger == nil {
		logger = slog.Default()
	}
	return &RetryStream{
		rdb:      rdb,
		stream:   stream,
		group:    group,
		consumer: consumer,
		logger:   logger,
	}
}

// enabled diz se há Redis configurado para o stream.
func (rs *RetryStream) enabled() bool { return rs != nil && rs.rdb != nil }

// Publish faz XADD do ID do webhook no stream. Fail-open: erro só loga (o
// polling do Postgres garante o reprocesso). Nunca derruba a request crítica.
func (rs *RetryStream) Publish(ctx context.Context, webhookID WebhookEventID) {
	if !rs.enabled() || webhookID == "" {
		return
	}
	// Timeout curto: a publicação não pode pendurar o fluxo de webhook.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// MAXLEN aproximado evita o stream crescer sem limite caso o consumer caia.
	if err := rs.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: rs.stream,
		MaxLen: 10000,
		Approx: true,
		Values: map[string]any{retryStreamField: string(webhookID)},
	}).Err(); err != nil {
		rs.logger.Warn("retryStream: XADD falhou (segue no polling do Postgres)",
			"err", err, "webhookID", webhookID)
	}
}

// ensureGroup cria o consumer group (idempotente). MKSTREAM cria o stream se
// ainda não existir. Ignora BUSYGROUP (grupo já existe).
func (rs *RetryStream) ensureGroup(ctx context.Context) error {
	err := rs.rdb.XGroupCreateMkStream(ctx, rs.stream, rs.group, "$").Err()
	if err != nil && !isBusyGroup(err) {
		return err
	}
	return nil
}

func isBusyGroup(err error) bool {
	// go-redis devolve o erro do servidor como texto; BUSYGROUP é o prefixo.
	return err != nil && len(err.Error()) >= 9 && err.Error()[:9] == "BUSYGROUP"
}

// Consume roda o loop de consumo do stream até o contexto ser cancelado.
// handle é o reprocessador (recebe o ID do webhook). XACK só ocorre quando
// handle retorna nil (at-least-once: falha → sem ACK → reentrega futura via
// XREADGROUP de pendentes ou, no pior caso, pelo polling do Postgres).
//
// recover() protege o loop: um panic no handle não derruba o consumidor nem o
// processo. Em caso de Redis indisponível, o loop faz backoff e segue tentando;
// o polling do worker cobre a janela em que o stream estiver fora.
func (rs *RetryStream) Consume(ctx context.Context, handle func(ctx context.Context, webhookID WebhookEventID) error) {
	if !rs.enabled() {
		rs.logger.Info("retryStream: Redis ausente, consumo desabilitado (fallback: polling do Postgres)")
		return
	}

	// Cria o grupo antes de consumir (com retry curto se o Redis ainda subindo).
	for {
		if err := rs.ensureGroup(ctx); err != nil {
			rs.logger.Warn("retryStream: falha ao criar consumer group, tentando de novo", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
		break
	}

	rs.logger.Info("retryStream: consumidor iniciado",
		"stream", rs.stream, "group", rs.group, "consumer", rs.consumer)

	// Primeiro drena pendentes deste consumer (mensagens lidas mas não ACKadas
	// numa execução anterior) lendo de "0"; depois passa a ler novas (">").
	readID := "0"
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		res, err := rs.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    rs.group,
			Consumer: rs.consumer,
			Streams:  []string{rs.stream, readID},
			Count:    20,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, redis.Nil) {
				// Sem mensagens na janela de Block; ao ler pendentes ("0") e não
				// haver mais, passamos a ler novas (">").
				if readID == "0" {
					readID = ">"
				}
				continue
			}
			rs.logger.Warn("retryStream: XREADGROUP falhou (backoff; polling cobre)", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		processed := 0
		for _, stream := range res {
			for _, msg := range stream.Messages {
				rs.handleOne(ctx, msg, handle)
				processed++
			}
		}

		// Terminou de drenar os pendentes ("0") → passa a ler novas.
		if readID == "0" && processed < 20 {
			readID = ">"
		}
	}
}

// handleOne processa uma entrada do stream com recover() e dá XACK só em sucesso.
func (rs *RetryStream) handleOne(ctx context.Context, msg redis.XMessage, handle func(ctx context.Context, webhookID WebhookEventID) error) {
	var procErr error
	defer func() {
		if r := recover(); r != nil {
			rs.logger.Error("retryStream: panic ao processar entrada", "panic", r, "msgID", msg.ID)
			return // sem ACK → reentrega
		}
		if procErr != nil {
			rs.logger.Error("retryStream: reprocessamento falhou (sem ACK; polling cobre)",
				"err", procErr, "msgID", msg.ID)
			return // sem ACK → reentrega
		}
		// Sucesso: ACK. Timeout próprio para não pendurar o loop.
		ackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rs.rdb.XAck(ackCtx, rs.stream, rs.group, msg.ID).Err(); err != nil {
			rs.logger.Warn("retryStream: XACK falhou (entrada pode ser reprocessada)", "err", err, "msgID", msg.ID)
		}
	}()

	id, _ := msg.Values[retryStreamField].(string)
	if id == "" {
		// Entrada malformada: ACK (não há o que reprocessar) — feito no defer ao
		// deixar procErr=nil.
		return
	}
	procErr = handle(ctx, WebhookEventID(id))
}
