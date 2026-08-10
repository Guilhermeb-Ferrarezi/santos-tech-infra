import * as Sentry from "@sentry/bun"
import { migrate } from "./db"
import { initAutoReply } from "./redis"
import { startWhatsApp } from "./wa"
import { startHTTP } from "./http"
import { startInboxWatcher } from "./inboxWatcher"

// SENTRY_DSN ausente (dev local) = no-op silencioso, sem travar o boot.
if (process.env.SENTRY_DSN) {
  Sentry.init({
    dsn: process.env.SENTRY_DSN,
    environment: process.env.SENTRY_ENVIRONMENT || "production",
  })
}

async function main() {
  await migrate()
  await initAutoReply()
  startHTTP()
  startInboxWatcher()
  await startWhatsApp()
}

main().catch(async (e) => {
  console.error("boot falhou", e)
  Sentry.captureException(e)
  await Sentry.flush(2000)
  process.exit(1)
})
