import * as Sentry from "@sentry/react"

/**
 * Segmentos de URL que SÃO segredo, não identificador: quem tem o token na mão
 * abre o checkout. Aparecem tanto na URL da página (`/pay/:token`,
 * `/link/:token`) quanto nas chamadas de API equivalentes (ver src/lib/api.ts).
 */
const SECRET_PATH_SEGMENTS = /\/(pay|link|subscribe)\/[^/?#]+/gi

/**
 * Troca o token pelo literal `[redacted]` mantendo o resto da URL — o path
 * continua legível pra depuração ("estourou no /pay"), mas o segredo não sai
 * daqui.
 */
function scrubUrl<T extends string | undefined | null>(raw: T): T {
  if (typeof raw !== "string") return raw
  return raw.replace(SECRET_PATH_SEGMENTS, (_full, seg: string) => `/${seg}/[redacted]`) as T
}

/**
 * Inicializa o Sentry se VITE_SENTRY_DSN estiver setada no build. Sem a env
 * var (dev local, ou preview sem o secret), é um no-op silencioso.
 *
 * App de pagamento: nada de replay. As integrações padrão do @sentry/react
 * gravam breadcrumb de fetch/xhr/navegação COM a URL completa — o comentário
 * antigo prometia "sem breadcrumbs de rede", mas o código não fazia nada a
 * respeito e os tokens de `/pay/{token}` e `/link/{token}` iriam inteiros pro
 * Sentry. beforeSend/beforeBreadcrumb abaixo raspam esses segmentos.
 *
 * Dados de cartão (PAN/CVV/validade) não passam por aqui em hipótese nenhuma:
 * nunca entram em estado global nem em URL, vão direto do formulário pra lib
 * da Efí (ver efi.ts).
 */
export function initSentry() {
  const dsn = import.meta.env.VITE_SENTRY_DSN
  if (!dsn) return
  Sentry.init({
    dsn,
    environment: import.meta.env.MODE,
    sendDefaultPii: false,

    beforeSend(event) {
      if (event.request?.url) event.request.url = scrubUrl(event.request.url)
      if (event.request?.headers?.Referer) {
        event.request.headers.Referer = scrubUrl(event.request.headers.Referer)
      }
      if (event.transaction) event.transaction = scrubUrl(event.transaction)
      return event
    },

    beforeBreadcrumb(breadcrumb) {
      const data = breadcrumb.data
      if (data) {
        // fetch/xhr guardam `url`; navegação (history) guarda `from`/`to`.
        if (typeof data.url === "string") data.url = scrubUrl(data.url)
        if (typeof data.from === "string") data.from = scrubUrl(data.from)
        if (typeof data.to === "string") data.to = scrubUrl(data.to)
      }
      if (typeof breadcrumb.message === "string") {
        breadcrumb.message = scrubUrl(breadcrumb.message)
      }
      return breadcrumb
    },
  })
}
