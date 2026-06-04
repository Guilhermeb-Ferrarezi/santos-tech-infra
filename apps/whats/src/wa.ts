import { makeWASocket, useMultiFileAuthState, DisconnectReason } from "baileys"
import type { WASocket } from "baileys"
import { config } from "./config"
import { decideMessage } from "./router"
import { allowlistSet, conversationFor, recordSeen, saveMessage } from "./db"
import { autoReplyEnabled, pushSim, setSimTyping } from "./redis"
import { runTurn } from "./agent"
import { emitEvent } from "./events"
import { extractEscalation } from "./escalate"
import { sendEmail } from "./email"

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
    if (u.qr || u.connection) emitEvent("status")
  })

  sock.ev.on("messages.upsert", async ({ messages }) => {
    for (const msg of messages) {
      const jid = msg.key.remoteJid ?? ""
      const text = msg.message?.conversation ?? msg.message?.extendedTextMessage?.text ?? ""
      const hasMedia = !!(msg.message?.imageMessage || msg.message?.audioMessage || msg.message?.videoMessage || msg.message?.documentMessage)
      // Radar: registra todo chat que mandou mensagem (mesmo fora da allowlist) pra
      // UI listar e permitir com um clique. Ignora status e canais.
      if (!msg.key.fromMe && jid && !jid.endsWith("@broadcast") && !jid.endsWith("@newsletter")) {
        recordSeen(jid, msg.pushName ?? "", text || (hasMedia ? "[mídia]" : ""))
          .then(() => emitEvent("seen"))
          .catch((e) => console.error("recordSeen falhou", jid, e))
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
      // Transcript do visor: a mensagem passou do filtro (chat permitido) — grava a entrada.
      saveMessage(jid, "in", text || "[mídia]")
        .then(() => emitEvent("message", { jid }))
        .catch((e) => console.error("saveMessage falhou", jid, e))
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
    // Escalação: o modelo sinaliza [[escalar: motivo]] e o harness manda o email —
    // o marcador nunca chega ao contato.
    const { text: clean, reason } = extractEscalation(reply)
    if (reason !== null) void escalate(jid, text, reason, clean)
    if (clean) await send(jid, clean)
  } catch (e) {
    console.error("turno falhou", jid, e) // silêncio: não responde quebrado
  } finally {
    await presence(jid, false).catch(() => {})
    inFlight.delete(jid)
  }
}

// Saída plugável: chats do simulador escrevem no transcript Redis em vez do WhatsApp.
// É o que permite a bancada do dashboard usar o MESMO bufferTurn/fireTurn dos reais.
// escalate avisa o dono por email que o agente precisou de ajuda num chat.
async function escalate(jid: string, question: string, reason: string, replied: string) {
  if (!config.escalateEmail) {
    console.error("escalação sinalizada mas ESCALATE_EMAIL não configurado", jid)
    return
  }
  const who = jid.endsWith("@simulator") ? `Simulador (${jid})` : jid
  const body =
    `O agente do WhatsApp pediu ajuda.\n\n` +
    `Chat: ${who}\n` +
    `Motivo: ${reason || "(não informado)"}\n\n` +
    `Mensagem recebida:\n${question}\n\n` +
    (replied ? `Resposta enviada ao contato:\n${replied}\n` : `Nenhuma resposta foi enviada ao contato.\n`)
  try {
    // A referência [whats:<jid>] no assunto permite responder o email e o texto
    // voltar pro chat certo (ver inboxWatcher.ts).
    await sendEmail(config.escalateEmail, `WhatsApp: agente pediu ajuda (${who}) [whats:${jid}]`, body)
  } catch (e) {
    console.error("falha ao enviar email de escalação", jid, e)
  }
}

// deliverOwnerReply: a resposta que o dono mandou por email vira NOTA INTERNA da
// conversa — o agente a transmite no tom dele (e a informação fica no contexto
// pra perguntas futuras). Reusa o fireTurn: lock, typing, escalação e envio.
export async function deliverOwnerReply(jid: string, info: string): Promise<void> {
  const prompt =
    `[nota interna do Guilherme — o contato NÃO viu isto e NÃO mandou mensagem agora] ` +
    `Resposta pra dúvida pendente deste chat: "${info}". ` +
    `Transmita essa informação ao contato agora, no seu tom natural de chat, curto. ` +
    `Não mencione email, nota interna nem que alguém te passou isso.`
  await fireTurn(jid, prompt, true)
}

async function send(jid: string, text: string) {
  if (jid.endsWith(SIM_SUFFIX)) {
    await pushSim(simUID(jid), "bot", text)
    return
  }
  await sock?.sendMessage(jid, { text })
  // Transcript do visor: resposta enviada num chat real.
  saveMessage(jid, "out", text)
    .then(() => emitEvent("message", { jid }))
    .catch((e) => console.error("saveMessage falhou", jid, e))
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
