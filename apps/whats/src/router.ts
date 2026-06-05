const MEDIA_REPLY = "opa, esse tipo de arquivo eu ainda não consigo abrir — me manda escrito?"
const OVERSIZE_REPLY = "opa, esse arquivo ficou grande demais pra mim — consegue mandar menor ou escrito?"

export interface MediaInput {
  kind: "image" | "pdf" | "audio" | "video"
  claudeReadable: boolean
  oversize: boolean
}

export interface MessageInput {
  jid: string
  fromMe: boolean
  ownerJID: string
  text: string
  media: MediaInput | null
  allowlist: Set<string>
  autoReplyEnabled: boolean
}

export type Decision =
  | { action: "ignore" }
  | { action: "reply_static"; text: string }
  | { action: "run_turn"; text: string; toolsDisabled: boolean; attach: boolean }

// decideMessage é pura: nenhuma I/O. O download, debounce e envio ficam no chamador (wa.ts).
export function decideMessage(m: MessageInput): Decision {
  const isOwnerChat = m.jid === m.ownerJID
  if (!m.autoReplyEnabled) return { action: "ignore" }
  if (m.fromMe && !isOwnerChat) return { action: "ignore" }
  if (!m.allowlist.has(m.jid)) return { action: "ignore" }
  if (m.media) {
    if (m.media.oversize) return { action: "reply_static", text: OVERSIZE_REPLY }
    if (!m.media.claudeReadable) return { action: "reply_static", text: MEDIA_REPLY }
    // Imagem/PDF: vai pro Claude como anexo (visão) — caption vira o texto do turno.
    return { action: "run_turn", text: m.text, toolsDisabled: !isOwnerChat, attach: true }
  }
  if (!m.text.trim()) return { action: "ignore" }
  return { action: "run_turn", text: m.text, toolsDisabled: !isOwnerChat, attach: false }
}
