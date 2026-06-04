// Escalação: o modelo (sem nenhuma ferramenta) sinaliza com um marcador no texto;
// quem AGE é o harness — escopo fixo: um email pro dono. Mantém o gate de tools
// intacto para terceiros.

const MARKER = /\s*\[\[\s*escalar\s*(?::([^\]]*))?\]\]\s*/i

export interface Escalation {
  text: string // resposta limpa (sem o marcador) — vazia = não responder
  reason: string | null // null = sem escalação
}

// extractEscalation é pura: detecta e remove o marcador [[escalar: motivo]].
export function extractEscalation(reply: string): Escalation {
  const m = MARKER.exec(reply)
  if (!m) return { text: reply, reason: null }
  return { text: reply.replace(MARKER, " ").trim(), reason: (m[1] ?? "").trim() }
}
