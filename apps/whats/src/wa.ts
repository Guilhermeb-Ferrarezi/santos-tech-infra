import { makeWASocket, useMultiFileAuthState, DisconnectReason, downloadMediaMessage } from "baileys"
import type { WASocket } from "baileys"
import { config } from "./config"
import { decideMessage } from "./router"
import { classifyMedia, saveMedia, MAX_MEDIA_BYTES, type MediaKind } from "./media"
import { allowlistSet, conversationFor, recordSeen, saveMessage, insertPendingKnowledge, answerLatestPending } from "./db"
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

// Anexo de mídia do turno (base64) — espelha o campo attachments do WS do agent-go.
export interface Attachment {
  mediaType: string
  data: string
}

const MAX_TURN_ATTACHMENTS = 4
const buffers = new Map<string, { text: string; attachments: Attachment[]; timer: ReturnType<typeof setTimeout> }>()
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
      // Mídia: classifica pelo mimetype/tamanho declarados (download só depois do filtro).
      const raw =
        msg.message?.imageMessage ?? msg.message?.videoMessage ?? msg.message?.audioMessage ?? msg.message?.documentMessage
      const media = raw ? classifyMedia(raw.mimetype ?? "", Number(raw.fileLength ?? 0)) : null
      const caption =
        msg.message?.imageMessage?.caption ?? msg.message?.videoMessage?.caption ?? msg.message?.documentMessage?.caption ?? ""
      const hasMedia = !!(msg.message?.imageMessage || msg.message?.audioMessage || msg.message?.videoMessage || msg.message?.documentMessage)
      // Radar: registra todo chat que mandou mensagem (mesmo fora da allowlist) pra
      // UI listar e permitir com um clique. Ignora status e canais.
      if (!msg.key.fromMe && jid && !jid.endsWith("@broadcast") && !jid.endsWith("@newsletter")) {
        recordSeen(jid, msg.pushName ?? "", text || caption || (hasMedia ? "[mídia]" : ""))
          .then(() => emitEvent("seen"))
          .catch((e) => console.error("recordSeen falhou", jid, e))
      }
      const d = decideMessage({
        jid,
        fromMe: !!msg.key.fromMe,
        ownerJID: config.ownerJID,
        text: text || caption,
        media,
        allowlist: await allowlistSet(),
        autoReplyEnabled: await autoReplyEnabled(),
      })
      if (d.action === "ignore") continue
      // Download/registro: só pra chat que passou do filtro (não baixamos mídia de
      // chat fora da allowlist). Falha no download degrada pra estático.
      let saved: { id: string; kind: MediaKind } | null = null
      let attachment: Attachment | null = null
      if (media && !media.oversize) {
        try {
          const buf = (await downloadMediaMessage(msg, "buffer", {})) as Buffer
          saved = { id: await saveMedia(buf, media.ext), kind: media.kind }
          if (media.claudeReadable && buf.length <= MAX_MEDIA_BYTES)
            attachment = { mediaType: (raw?.mimetype ?? "").split(";")[0]!.trim(), data: buf.toString("base64") }
        } catch (e) {
          console.error("download de mídia falhou", jid, e)
        }
      }
      // Transcript do visor: a mensagem passou do filtro (chat permitido) — grava a entrada.
      saveMessage(jid, "in", text || caption || (media ? `[${media.kind}]` : "[mídia]"), saved ?? undefined)
        .then(() => emitEvent("message", { jid }))
        .catch((e) => console.error("saveMessage falhou", jid, e))
      if (d.action === "reply_static") { await send(jid, d.text); continue }
      if (d.attach && !attachment) {
        await send(jid, "opa, não consegui abrir esse arquivo — me manda de novo?")
        continue
      }
      const turnText = d.text.trim() || (media ? defaultMediaText(media.kind) : "")
      bufferTurn(jid, turnText, d.toolsDisabled, attachment ? [attachment] : [])
    }
  })
}

// defaultMediaText dá contexto ao modelo quando a mídia veio sem legenda.
function defaultMediaText(kind: MediaKind): string {
  return kind === "pdf" ? "(o contato enviou um PDF, sem legenda)" : "(o contato enviou uma imagem, sem legenda)"
}

function bufferTurn(jid: string, text: string, toolsDisabled: boolean, attachments: Attachment[] = []) {
  const cur = buffers.get(jid)
  if (cur) clearTimeout(cur.timer)
  const merged = cur ? cur.text + "\n" + text : text
  // Cap de anexos por turno: o excedente é descartado com log (limite da visão).
  const atts = [...(cur?.attachments ?? []), ...attachments].slice(0, MAX_TURN_ATTACHMENTS)
  if ((cur?.attachments.length ?? 0) + attachments.length > MAX_TURN_ATTACHMENTS)
    console.error("anexos além do limite descartados", jid)
  const timer = setTimeout(() => { buffers.delete(jid); void fireTurn(jid, merged, toolsDisabled, atts) }, config.debounceMs)
  buffers.set(jid, { text: merged, attachments: atts, timer })
}

// fireTurn roda o turno e envia a resposta; devolve o texto entregue (null se
// nada foi enviado) — usado pela memória pra guardar a resposta final.
async function fireTurn(jid: string, text: string, toolsDisabled: boolean, attachments: Attachment[] = []): Promise<string | null> {
  if (inFlight.has(jid)) return null // rate limit: 1 turno por chat por vez
  inFlight.add(jid)
  try {
    await presence(jid, true)
    const isFirst = (await conversationFor(jid)) === null
    const reply = await runTurn(jid, text, toolsDisabled, isFirst, attachments)
    // Escalação: o modelo sinaliza [[escalar: motivo]] e o harness manda o email —
    // o marcador nunca chega ao contato.
    const { text: clean, reason } = extractEscalation(reply)
    if (reason !== null) void escalate(jid, text, reason, clean)
    if (clean) await send(jid, clean)
    return clean || null
  } catch (e) {
    console.error("turno falhou", jid, e) // silêncio: não responde quebrado
    return null
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
  // Memória auto-aprendida: a pergunta fica pendente; a resposta do dono (por
  // email) completa o par P→R e vira fato consultado nos próximos turnos.
  insertPendingKnowledge(jid, question, reason).catch((e) => console.error("pendência de memória falhou", jid, e))
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
    `Sobre a pendência deste chat, o Guilherme disse: "${info}". ` +
    `Interprete: se for uma INFORMAÇÃO (ex.: um valor, um endereço, um sim/não), transmita-a ao ` +
    `contato no seu tom natural de chat, curto. Se for uma INSTRUÇÃO (ex.: 'pesquisa no google X', ` +
    `'pergunta pra ele Y', 'fala que Z'), EXECUTE-A com as capacidades que você tem (ex.: WebSearch ` +
    `quando a busca estiver ligada) e responda o contato com o resultado. ` +
    `Não mencione email, nota interna nem que alguém te passou isso.`
  const delivered = await fireTurn(jid, prompt, true)
  // Memória: guarda a RESPOSTA FINAL entregue (sem a assinatura em itálico) —
  // se o turno falhar, cai pro texto cru do dono pra não perder o aprendizado.
  const answer = (delivered ?? "").replace(/\n*_[^_\n]+_\s*$/, "").trim() || info
  await answerLatestPending(jid, answer).catch((e) => {
    console.error("gravar resposta na memória falhou", jid, e)
    return false
  })
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
