import { makeWASocket, useMultiFileAuthState, DisconnectReason } from "baileys"
import type { WASocket } from "baileys"
import { config } from "./config"
import { decideMessage } from "./router"
import { allowlistSet, conversationFor, recordSeen } from "./db"
import { autoReplyEnabled } from "./redis"
import { runTurn } from "./agent"

type Status = "disconnected" | "waiting_qr" | "connected"

let sock: WASocket | null = null
let status: Status = "disconnected"
let lastQR: string | null = null
let pairedNumber: string | null = null

const buffers = new Map<string, { text: string; timer: ReturnType<typeof setTimeout> }>()
const inFlight = new Set<string>()

export function waStatus() {
  return { status, qr: lastQR, number: pairedNumber }
}

export async function startWhatsApp(): Promise<void> {
  const { state, saveCreds } = await useMultiFileAuthState(config.sessionDir)
  sock = makeWASocket({ auth: state, printQRInTerminal: false })

  sock.ev.on("creds.update", saveCreds)
  sock.ev.on("connection.update", (u) => {
    if (u.qr) { lastQR = u.qr; status = "waiting_qr" }
    if (u.connection === "open") {
      status = "connected"; lastQR = null
      pairedNumber = sock?.user?.id?.split(":")[0] ?? null
    }
    if (u.connection === "close") {
      status = "disconnected"
      const code = (u.lastDisconnect?.error as any)?.output?.statusCode
      // backoff de 3s evita martelar o servidor do WhatsApp em loop de reconexão
      if (code !== DisconnectReason.loggedOut) setTimeout(() => startWhatsApp().catch(() => {}), 3000)
    }
  })

  sock.ev.on("messages.upsert", async ({ messages }) => {
    for (const msg of messages) {
      const jid = msg.key.remoteJid ?? ""
      const text = msg.message?.conversation ?? msg.message?.extendedTextMessage?.text ?? ""
      const hasMedia = !!(msg.message?.imageMessage || msg.message?.audioMessage || msg.message?.videoMessage || msg.message?.documentMessage)
      // Radar: registra todo chat que mandou mensagem (mesmo fora da allowlist) pra
      // UI listar e permitir com um clique. Ignora status e canais.
      if (!msg.key.fromMe && jid && !jid.endsWith("@broadcast") && !jid.endsWith("@newsletter")) {
        recordSeen(jid, msg.pushName ?? "", text || (hasMedia ? "[mídia]" : "")).catch((e) =>
          console.error("recordSeen falhou", jid, e),
        )
      }
      const d = decideMessage({
        jid,
        fromMe: !!msg.key.fromMe,
        ownerJID: config.ownerJID,
        text,
        hasMedia,
        allowlist: await allowlistSet(),
        autoReplyEnabled: await autoReplyEnabled(),
      })
      if (d.action === "ignore") continue
      if (d.action === "reply_static") { await send(jid, d.text); continue }
      bufferTurn(jid, d.text, d.toolsDisabled)
    }
  })
}

function bufferTurn(jid: string, text: string, toolsDisabled: boolean) {
  const cur = buffers.get(jid)
  if (cur) { clearTimeout(cur.timer); cur.text += "\n" + text }
  const merged = cur ? cur.text : text
  const timer = setTimeout(() => { buffers.delete(jid); void fireTurn(jid, merged, toolsDisabled) }, config.debounceMs)
  buffers.set(jid, { text: merged, timer })
}

async function fireTurn(jid: string, text: string, toolsDisabled: boolean) {
  if (inFlight.has(jid)) return // rate limit: 1 turno por chat por vez
  inFlight.add(jid)
  try {
    await sock?.sendPresenceUpdate("composing", jid)
    const isFirst = (await conversationFor(jid)) === null
    const reply = await runTurn(jid, text, toolsDisabled, isFirst)
    if (reply) await send(jid, reply)
  } catch (e) {
    console.error("turno falhou", jid, e) // silêncio: não responde quebrado
  } finally {
    await sock?.sendPresenceUpdate("paused", jid).catch(() => {})
    inFlight.delete(jid)
  }
}

async function send(jid: string, text: string) {
  await sock?.sendMessage(jid, { text })
}

export async function logoutWhatsApp(): Promise<void> {
  await sock?.logout().catch(() => {})
  status = "disconnected"; lastQR = null; pairedNumber = null
}
