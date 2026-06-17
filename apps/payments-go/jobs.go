package main

import (
	"encoding/json"
	"time"

	"github.com/hibiken/asynq"
)

// Nomes dos tasks asynq.
const (
	// TaskNotifyPaid dispara os emails (admin + recibo) na confirmação de um pagamento.
	TaskNotifyPaid = "payment:notify_paid"
)

// notifyDefaults: a notificação de pagamento é importante mas não crítica para o
// fluxo de pagamento em si — retenta algumas vezes com backoff (o asynq aplica
// backoff exponencial por padrão) e tem um teto de execução por task.
var notifyDefaults = []asynq.Option{
	asynq.MaxRetry(5),
	asynq.Timeout(60 * time.Second),
}

// NotifyPaidPayload carrega o token público da cobrança paga. Mantemos só o token
// (e não a charge inteira) para que o worker releia o estado atual do banco no
// momento do envio, evitando notificar com dados defasados.
type NotifyPaidPayload struct {
	PublicToken string `json:"public_token"`
}

func newNotifyPaidTask(token string) *asynq.Task {
	b, _ := json.Marshal(NotifyPaidPayload{PublicToken: token})
	return asynq.NewTask(TaskNotifyPaid, b, notifyDefaults...)
}
