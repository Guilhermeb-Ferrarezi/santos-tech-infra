// Memória auto-aprendida: escalações respondidas pelo dono viram fatos (P→R).
// rankFacts é pura (testável); a persistência fica no db.ts.

export interface Fact {
  id: number
  question: string
  answer: string
}

// Stopwords PT comuns — palavras que não carregam assunto.
const STOP = new Set([
  "a", "o", "e", "de", "da", "do", "das", "dos", "em", "no", "na", "nos", "nas", "um", "uma",
  "que", "qual", "quais", "com", "sem", "por", "pra", "para", "pro", "ao", "aos", "as", "os",
  "tem", "ter", "fica", "ser", "vai", "vou", "voce", "voces", "vcs", "vc", "eu", "ele", "ela",
  "meu", "minha", "seu", "sua", "isso", "esse", "essa", "este", "esta", "ano", "dia", "hoje",
  "oi", "ola", "opa", "bom", "boa", "tudo", "bem", "ai", "la", "ne", "sim", "nao", "mais",
])

function tokens(s: string): Set<string> {
  return new Set(
    s
      .toLowerCase()
      .normalize("NFD")
      .replace(/[̀-ͯ]/g, "")
      .split(/[^a-z0-9]+/)
      .filter((w) => w.length >= 3 && !STOP.has(w)),
  )
}

// rankFacts devolve até 3 fatos relevantes pra mensagem, por sobreposição de
// palavras significativas (pergunta pesa mais que resposta).
export function rankFacts(message: string, facts: Fact[]): Fact[] {
  const msg = tokens(message)
  if (msg.size === 0) return []
  return facts
    .map((f) => {
      const q = tokens(f.question)
      const a = tokens(f.answer)
      let score = 0
      for (const w of msg) {
        if (q.has(w)) score += 2
        if (a.has(w)) score += 1
      }
      return { f, score }
    })
    .filter((x) => x.score >= 2)
    .sort((x, y) => y.score - x.score)
    .slice(0, 3)
    .map((x) => x.f)
}

// factsNote formata o bloco de memória interna injetado no turno.
export function factsNote(facts: Fact[]): string {
  if (facts.length === 0) return ""
  const lines = facts.map((f) => `- P: ${f.question}\n  R: ${f.answer}`)
  return (
    `[memória interna — respostas já confirmadas pelo Guilherme; use-as com naturalidade ` +
    `e NÃO escale o que já está respondido aqui]\n${lines.join("\n")}\n\n`
  )
}
