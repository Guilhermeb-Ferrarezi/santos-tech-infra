import { test, expect } from "bun:test"
import { parseEscalationReply, stripQuotedReply } from "../src/reply"

const allowed = new Set(["guibferrarezi@gmail.com"])

test("extrai jid do assunto e o texto limpo", () => {
  const r = parseEscalationReply(
    {
      from: "guibferrarezi@gmail.com",
      subject: "Re: WhatsApp: agente pediu ajuda [whats:5511@s.whatsapp.net]",
      text: "Fala que é R$ 350 por mês.\n\nEm qua., 4 de jun., Claude <claude@santos-tech.com> escreveu:\n> O agente pediu ajuda",
    },
    allowed,
  )
  expect(r).not.toBeNull()
  expect(r!.jid).toBe("5511@s.whatsapp.net")
  expect(r!.message).toBe("Fala que é R$ 350 por mês.")
})

test("ignora remetente fora da lista", () => {
  const r = parseEscalationReply(
    { from: "spam@x.com", subject: "Re: [whats:5511@s.whatsapp.net]", text: "oi" },
    allowed,
  )
  expect(r).toBeNull()
})

test("ignora email sem referência de chat", () => {
  const r = parseEscalationReply(
    { from: "guibferrarezi@gmail.com", subject: "Re: outra coisa", text: "oi" },
    allowed,
  )
  expect(r).toBeNull()
})

test("stripQuotedReply remove citações, cabeçalho de reply e assinatura", () => {
  const text = "ok pode marcar\n\nOn Wed, Jun 4, John wrote:\n> can we meet?\n-- \nGuilherme"
  expect(stripQuotedReply(text)).toBe("ok pode marcar")
})

test("texto vazio após limpeza vira null", () => {
  const r = parseEscalationReply(
    { from: "guibferrarezi@gmail.com", subject: "Re: [whats:5511@s.whatsapp.net]", text: "> só citação" },
    allowed,
  )
  expect(r).toBeNull()
})
