import * as Sentry from "@sentry/react"

/**
 * Inicializa o Sentry se VITE_SENTRY_DSN estiver setada no build. Sem a env
 * var (dev local, ou preview sem o secret), é um no-op silencioso.
 */
export function initSentry() {
  const dsn = import.meta.env.VITE_SENTRY_DSN
  if (!dsn) return
  Sentry.init({
    dsn,
    environment: import.meta.env.MODE,
  })
}
