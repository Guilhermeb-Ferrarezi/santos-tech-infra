import { migrate } from "./db"
import { initAutoReply } from "./redis"
import { startWhatsApp } from "./wa"
import { startHTTP } from "./http"
import { startInboxWatcher } from "./inboxWatcher"

async function main() {
  await migrate()
  await initAutoReply()
  startHTTP()
  startInboxWatcher()
  await startWhatsApp()
}

main().catch((e) => { console.error("boot falhou", e); process.exit(1) })
