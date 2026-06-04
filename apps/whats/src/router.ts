const MEDIA_REPLY = "opa, só consigo ler texto por aqui ainda — me manda escrito?"

export interface MessageInput {
  jid: string
  fromMe: boolean
  ownerJID: string
  text: string
  hasMedia: boolean
  allowlist: Set<string>
  autoReplyEnabled: boolean
}

export type Decision =
  | { action: "ignore" }
  | { action: "reply_static"; text: string }
  | { action: "run_turn"; text: string; toolsDisabled: boolean }

// decideMessage é pura: nenhuma I/O. O debounce e o envio ficam no chamador (wa.ts).
export function decideMessage(m: MessageInput): Decision {
  const isOwnerChat = m.jid === m.ownerJID
  if (!m.autoReplyEnabled) return { action: "ignore" }
  if (m.fromMe && !isOwnerChat) return { action: "ignore" }
  if (!m.allowlist.has(m.jid)) return { action: "ignore" }
  if (m.hasMedia) return { action: "reply_static", text: MEDIA_REPLY }
  if (!m.text.trim()) return { action: "ignore" }
  return { action: "run_turn", text: m.text, toolsDisabled: !isOwnerChat }
}
