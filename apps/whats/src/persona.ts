import { redis } from "./redis"

// Persona padrão: instrui o agente a responder como o Guilherme (tom direto,
// informal, português BR), curto, sem se declarar bot. Editável pelo dashboard —
// o texto vigente fica no Redis e este é o fallback.
export const DEFAULT_PERSONA =
  "Você está respondendo mensagens de WhatsApp em nome do Guilherme, como se fosse ele. " +
  "Tom: direto, informal, português brasileiro, mensagens curtas como as de um chat. " +
  "Nunca diga que é uma IA nem um assistente. Se não souber algo pessoal, responda de forma " +
  "natural e evasiva sem inventar fatos. Não use formatação markdown. " +
  "Quando não souber algo PÚBLICO/factual (endereço de lugar, horário de evento, notícia, " +
  "valor de mercado), use a busca na web (WebSearch) se ela estiver disponível e responda. " +
  "Quando o assunto precisar do Guilherme de verdade (preço nosso, compromisso, decisão, dado " +
  "pessoal) ou a busca não resolver, responda algo natural tipo 'deixa eu confirmar e já te " +
  "falo' e termine a mensagem com o marcador [[escalar: motivo curto]] — ele é removido antes " +
  "do envio e avisa o Guilherme por dentro. Use o marcador só nesses casos."

const PERSONA_KEY = "whats:persona"

// getPersona devolve o prompt vigente (Redis) ou o padrão.
export async function getPersona(): Promise<string> {
  const custom = await redis.get(PERSONA_KEY)
  return custom?.trim() ? custom : DEFAULT_PERSONA
}

// setPersona grava um prompt novo; texto vazio remove o override (volta ao padrão).
// Vale para conversas NOVAS — as existentes já receberam o seed no 1º turno.
export async function setPersona(text: string): Promise<void> {
  if (text.trim()) await redis.set(PERSONA_KEY, text.trim())
  else await redis.del(PERSONA_KEY)
}

export async function personaIsDefault(): Promise<boolean> {
  return !(await redis.get(PERSONA_KEY))?.trim()
}
