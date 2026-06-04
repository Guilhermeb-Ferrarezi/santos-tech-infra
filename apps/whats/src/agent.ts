import WebSocket from "ws"
import { config } from "./config"
import { serviceToken } from "./jwt"
import { PERSONA_SEED } from "./persona"
import { conversationFor, linkChat } from "./db"

interface TurnEvent {
  type: string
  text?: string
  data?: { result?: unknown }
  message?: string
}

// finalTextFromEvents extrai o texto da resposta: prioriza o "result", senão
// concatena os deltas. Pura — testável sem rede.
export function finalTextFromEvents(events: TurnEvent[]): string {
  const result = events.find((e) => e.type === "result")
  const r = result?.data?.result
  if (typeof r === "string" && r.trim()) return r.trim()
  return events.filter((e) => e.type === "delta").map((e) => e.text ?? "").join("").trim()
}

// ensureConversation devolve a conversation_id do chat, criando no agent-go se for o
// primeiro contato. toolsDisabled controla o gate de ferramentas no spawn.
async function ensureConversation(jid: string, toolsDisabled: boolean): Promise<string> {
  const existing = await conversationFor(jid)
  if (existing) return existing
  const res = await fetch(`${config.agentURL}/conversations`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${serviceToken()}` },
    body: JSON.stringify({ title: `WhatsApp ${jid}`, toolsDisabled }),
  })
  if (!res.ok) throw new Error(`criar conversa falhou: HTTP ${res.status}`)
  const conv = (await res.json()) as { id: string }
  await linkChat(jid, conv.id)
  return conv.id
}

// runTurn roda um turno e devolve o texto final. Abre o WS, manda o prompt (com seed
// no 1º turno de conversa nova) e coleta eventos até "done".
export async function runTurn(jid: string, text: string, toolsDisabled: boolean, isFirst: boolean): Promise<string> {
  const convID = await ensureConversation(jid, toolsDisabled)
  const wsURL = `${config.agentURL.replace(/^http/, "ws")}/conversations/${convID}/ws`
  const ws = new WebSocket(wsURL, { headers: { Authorization: `Bearer ${serviceToken()}` } })
  const events: TurnEvent[] = []
  const prompt = isFirst ? `${PERSONA_SEED}\n\n---\n\nMensagem recebida: ${text}` : text

  return new Promise<string>((resolve, reject) => {
    const timer = setTimeout(() => { ws.close(); reject(new Error("turno expirou")) }, 120_000)
    ws.on("open", () => ws.send(JSON.stringify({ type: "prompt", text: prompt })))
    ws.on("message", (raw) => {
      const ev = JSON.parse(raw.toString()) as TurnEvent
      events.push(ev)
      if (ev.type === "done") { clearTimeout(timer); ws.close(); resolve(finalTextFromEvents(events)) }
      if (ev.type === "error") { clearTimeout(timer); ws.close(); reject(new Error(`turno erro: ${ev.message}`)) }
    })
    ws.on("error", (e) => { clearTimeout(timer); reject(e) })
  })
}
