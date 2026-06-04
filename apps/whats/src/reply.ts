// Reply-to-WhatsApp: o dono responde o email de escalação e o texto vai (quase)
// literal pro chat. Funções puras — o watcher (inboxWatcher.ts) faz o I/O.

const JID_REF = /\[whats:([^\]]+)\]/i

export interface InboundEmail {
  from: string
  subject: string
  text: string
}

export interface EscalationReply {
  jid: string
  message: string
}

// stripQuotedReply tira a parte citada do reply de email: linhas ">", o cabeçalho
// "Em ... escreveu:" / "On ... wrote:" e assinatura "-- ". Sobra o que foi digitado.
export function stripQuotedReply(text: string): string {
  const lines = text.split(/\r?\n/)
  const out: string[] = []
  for (const line of lines) {
    if (/^\s*>/.test(line)) continue
    if (/^(Em|On)\b.*\b(escreveu|wrote):\s*$/i.test(line)) break
    if (/^--\s*$/.test(line)) break
    out.push(line)
  }
  return out.join("\n").trim()
}

// parseEscalationReply valida remetente (fail-closed), extrai o jid do assunto e o
// texto limpo. null = não é um reply de escalação processável.
export function parseEscalationReply(mail: InboundEmail, allowedSenders: Set<string>): EscalationReply | null {
  if (!allowedSenders.has(mail.from.trim().toLowerCase())) return null
  const m = JID_REF.exec(mail.subject)
  if (!m?.[1]) return null
  const message = stripQuotedReply(mail.text)
  if (!message) return null
  return { jid: m[1].trim(), message }
}
