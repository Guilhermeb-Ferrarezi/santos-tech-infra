import { test, expect } from "bun:test"
import { rankFacts } from "../src/knowledge"

const facts = [
  { id: 1, question: "quanto custa a mensalidade do CREATE?", answer: "R$ 329,90; com 2 irmãos, 300 cada" },
  { id: 2, question: "vocês dão certificado no final do curso?", answer: "sim, certificado digital incluso" },
  { id: 3, question: "tem aula de sábado de manhã?", answer: "sim, manhã inteira" },
]

test("acha o fato relevante pela sobreposição de palavras", () => {
  const top = rankFacts("oi! quanto fica a mensalidade pro ano que vem?", facts)
  expect(top[0]!.id).toBe(1)
})

test("pergunta sobre certificado encontra o fato certo", () => {
  const top = rankFacts("voces emitem certificado?", facts)
  expect(top[0]!.id).toBe(2)
})

test("mensagem sem relação não devolve nada", () => {
  expect(rankFacts("bom dia, tudo bem?", facts)).toHaveLength(0)
})

test("ignora palavras curtas/stopwords no score", () => {
  const top = rankFacts("e ai, de boa? que dia tem aula?", facts)
  expect(top.length).toBeGreaterThan(0)
  expect(top[0]!.id).toBe(3)
})
