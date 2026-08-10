package golog

import (
	"log/slog"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
)

// InitSentry inicializa o Sentry se SENTRY_DSN estiver setada. Sem a env var
// (dev local, ou serviço que ainda não tem projeto no Sentry), é um no-op
// silencioso — nunca trava o boot por falta de config de observabilidade.
//
// service identifica a origem do evento (nome do serviço, ex.: "api-go");
// vira a tag "service" em todo evento reportado, útil se algum dia vários
// serviços compartilharem um projeto Sentry (hoje cada um tem o seu).
func InitSentry(service string) {
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		return
	}
	env := os.Getenv("SENTRY_ENVIRONMENT")
	if env == "" {
		env = "production"
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      env,
		ServerName:       service,
		AttachStacktrace: true,
	})
	if err != nil {
		slog.Error("falha ao iniciar o Sentry", "err", err)
		return
	}
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("service", service)
	})
}

// FlushSentry drena os eventos pendentes antes do processo encerrar (panics,
// erros capturados perto do shutdown). Chame com defer logo após InitSentry
// no main(). No-op se o Sentry não foi inicializado.
func FlushSentry() {
	sentry.Flush(2 * time.Second)
}

// sentryRecover reporta um panic capturado pelo RequestLogger ao Sentry (além
// do log estruturado que já acontece). No-op se o Sentry não foi inicializado.
func sentryRecover(rec any) {
	if sentry.CurrentHub().Client() == nil {
		return
	}
	sentry.CurrentHub().Recover(rec)
}

// CaptureError reporta um erro de negócio (não-panic) ao Sentry — use nos
// pontos onde já se faz slog.Error(..., "err", err) e o erro é inesperado o
// bastante pra valer investigação (não em caminhos de validação/4xx normais).
// No-op se o Sentry não foi inicializado.
func CaptureError(err error) {
	if err == nil || sentry.CurrentHub().Client() == nil {
		return
	}
	sentry.CaptureException(err)
}
