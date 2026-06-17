package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
)

// Fila durável (asynq sobre o MESMO Redis) para os emails fire-and-forget. Antes
// esses envios rodavam em goroutines soltas (safeGo) que se perdiam num restart
// do processo. Agora o envio vira uma task persistida no Redis: sobrevive a
// restart e é reprocessada com retry pelo asynq.Server embutido neste processo.

// TaskEmailSend é o tipo de task de envio de email.
const TaskEmailSend = "email:send"

// emailQueueConcurrency: poucos workers bastam — o gargalo é a API de email
// (rede externa), não CPU. Mantém pressão baixa sobre o provedor.
const emailQueueConcurrency = 5

// Opções por task de email: até 3 retentativas (a API de email pode estar
// momentaneamente fora) e teto de execução por task acima do timeout do cliente
// HTTP de email (20s) para a tentativa caber inteira.
var emailTaskOpts = []asynq.Option{
	asynq.MaxRetry(3),
	asynq.Timeout(40 * time.Second),
}

// emailPayload é o corpo serializado da task de envio.
type emailPayload struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

// enqueueEmail enfileira um envio de email na fila durável. Se o enqueue falhar
// (Redis fora, p.ex.), apenas loga e segue — não derruba a request crítica que o
// disparou (o email é colateral; a durabilidade depende do Redis estar de pé).
// Substitui os antigos safeGo(...) { s.email.send(...) }.
func (s *Server) enqueueEmail(name, to, subject, html string) {
	if s.queue == nil {
		// Sem fila (modo degradado/teste): cai para envio direto fire-and-forget
		// com recover, preservando o comportamento antigo em vez de perder o email.
		safeGo(name, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if err := s.email.send(ctx, to, subject, html); err != nil {
				slog.Error("falha ao enviar email (fallback sem fila)", "where", name, "err", err)
			}
		})
		return
	}
	b, err := json.Marshal(emailPayload{To: to, Subject: subject, HTML: html})
	if err != nil {
		slog.Error("falha ao serializar task de email (segue)", "where", name, "err", err)
		return
	}
	if _, err := s.queue.Enqueue(asynq.NewTask(TaskEmailSend, b, emailTaskOpts...)); err != nil {
		slog.Error("falha ao enfileirar email (segue)", "where", name, "err", err)
	}
}

// handleEmailSend é o handler asynq do envio: desserializa o payload e chama o
// cliente de email. Erro devolvido → asynq agenda o retry (backoff exponencial).
func (s *Server) handleEmailSend(ctx context.Context, t *asynq.Task) error {
	var p emailPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		// Payload corrompido nunca vai dar certo: não adianta retentar (SkipRetry
		// descarta a task em vez de reagendá-la até esgotar as tentativas).
		slog.Error("task de email com payload inválido", "err", err)
		return asynq.SkipRetry
	}
	if err := s.email.send(ctx, p.To, p.Subject, p.HTML); err != nil {
		slog.Warn("envio de email falhou — asynq vai retentar", "subject", p.Subject, "err", err)
		return err
	}
	return nil
}

// newEmailQueueServer monta o asynq.Server embutido que processa a fila de emails.
// O caller liga (Run em goroutine) e desliga (Stop/Shutdown) no graceful shutdown.
func (s *Server) newEmailQueueServer(redisOpt asynq.RedisConnOpt) (*asynq.Server, *asynq.ServeMux) {
	srv := asynq.NewServer(redisOpt, asynq.Config{Concurrency: emailQueueConcurrency})
	mux := asynq.NewServeMux()
	mux.HandleFunc(TaskEmailSend, s.handleEmailSend)
	return srv, mux
}
