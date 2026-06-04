import { makeWASocket, useMultiFileAuthState, DisconnectReason } from "baileys"
import type { WASocket } from "baileys"
import { config } from "./config"
import { decideMessage } from "./router"
import { allowlistSet, conversationFor, recordSeen } from "./db"
import { autoReplyEnabled, pushSim, setSimTyping } from "./redis"
import { runTurn } from "./agent"

// JID sintético do simulador do dashboard: 1 chat de teste por admin.
const SIM_SUFFIX = "@simulator"
export const simJID = (uid: number) => `sim-${uid}${SIM_SUFFIX}`
const simUID = (jid: string) => Number(jid.slice(4, -SIM_SUFFIX.length))

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
    await presence(jid, true)
    const isFirst = (await conversationFor(jid)) === null
    const reply = await runTurn(jid, text, toolsDisabled, isFirst)
    if (reply) await send(jid, reply)
  } catch (e) {
    console.error("turno falhou", jid, e) // silêncio: não responde quebrado
  } finally {
    await presence(jid, false).catch(() => {})
    inFlight.delete(jid)
  }
}

// Saída plugável: chats do simulador escrevem no transcript Redis em vez do WhatsApp.
// É o que permite a bancada do dashboard usar o MESMO bufferTurn/fireTurn dos reais.
async function send(jid: string, text: string) {
  if (jid.endsWith(SIM_SUFFIX)) {
    await pushSim(simUID(jid), "bot", text)
    return
  }
  await sock?.sendMessage(jid, { text })
}

async function presence(jid: string, on: boolean) {
  if (jid.endsWith(SIM_SUFFIX)) {
    await setSimTyping(simUID(jid), on)
    return
  }
  await sock?.sendPresenceUpdate(on ? "composing" : "paused", jid)
}

// injectSimMessage alimenta o pipeline com uma bolha do simulador: grava no
// transcript e entra no mesmo debounce/agregação de um contato real, sempre como
// terceiro (tools desligadas). Ignora allowlist e o toggle global — é bancada.
export async function injectSimMessage(uid: number, text: string): Promise<void> {
  await pushSim(uid, "user", text)
  bufferTurn(simJID(uid), text, true)
}

export async function logoutWhatsApp(): Promise<void> {
  await sock?.logout().catch(() => {})
  status = "disconnected"; lastQR = null; pairedNumber = null
}
