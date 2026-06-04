import { config } from "./config"
import { serviceToken } from "./jwt"
import { parseEscalationReply } from "./reply"
import { redis } from "./redis"
import { deliverOwnerReply } from "./wa"

// Watcher da caixa do Claude (claude@santos-tech.com): a cada POLL_MS lê as
// mensagens não-lidas; replies de escalação do dono viram mensagem no chat
// referenciado no assunto ([whats:<jid>]). Tudo que foi avaliado é marcado como
// lido (a caixa é de uso exclusivo do agente).

const POLL_MS = 45_000
const PROCESSED_KEY = "whats:inbox:processed" // uids já avaliados (cinto + suspensório do seen)

interface MailSummary {
  uid: number
  from: string
  subject: string
  seen: boolean
}

function authHeader() {
  return { Authorization: `Bearer ${serviceToken(config.claudeUserID)}` }
}

async function listUnseen(): Promise<MailSummary[]> {
  const res = await fetch(`${config.mailsAPI}/mailbox/me/messages?limit=20`, { headers: authHeader() })
  if (!res.ok) throw new Error(`listar caixa falhou: HTTP ${res.status}`)
  const data = (await res.json()) as { messages: MailSummary[] }
  return (data.messages ?? []).filter((m) => !m.seen)
}

async function fetchMail(uid: number): Promise<{ from: string; subject: string; text: string; html?: string }> {
  const res = await fetch(`${config.mailsAPI}/mailbox/me/messages/${uid}`, { headers: authHeader() })
  if (!res.ok) throw new Error(`ler email ${uid} falhou: HTTP ${res.status}`)
  return (await res.json()) as { from: string; subject: string; text: string; html?: string }
}

async function markSeen(uid: number): Promise<void> {
  await fetch(`${config.mailsAPI}/mailbox/me/messages/${uid}/seen`, { method: "POST", headers: authHeader() }).catch(
    () => {},
  )
}

// htmlToText: fallback tosco pra reply só-HTML (Gmail manda text também, então é raro).
function htmlToText(html: string): string {
  return html
    .replace(/<br\s*\/?>/gi, "\n")
    .replace(/<\/p>/gi, "\n")
    .replace(/<[^>]+>/g, "")
    .replace(/&nbsp;/g, " ")
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
}

async function tick(allowed: Set<string>): Promise<void> {
  const unseen = await listUnseen()
  for (const m of unseen) {
    if (await redis.sismember(PROCESSED_KEY, String(m.uid))) continue
    try {
      const full = await fetchMail(m.uid)
      const text = full.text?.trim() ? full.text : htmlToText(full.html ?? "")
      const reply = parseEscalationReply({ from: full.from, subject: full.subject, text }, allowed)
      if (reply) {
        await deliverOwnerReply(reply.jid, reply.message)
        console.log("reply de escalação entregue", reply.jid)
      }
    } catch (e) {
      console.error("falha processando email", m.uid, e)
    } finally {
      await redis.sadd(PROCESSED_KEY, String(m.uid))
      await markSeen(m.uid)
    }
  }
}

export function startInboxWatcher(): void {
  if (!config.escalateEmail) return // sem escalação configurada, nada a vigiar
  const allowed = new Set(
    [config.escalateEmail, ...config.replySenders].map((s) => s.trim().toLowerCase()).filter(Boolean),
  )
  const loop = () => {
    tick(allowed)
      .catch((e) => console.error("inbox watcher falhou", e))
      .finally(() => setTimeout(loop, POLL_MS))
  }
  loop()
}
