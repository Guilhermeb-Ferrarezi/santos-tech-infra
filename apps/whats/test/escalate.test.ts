import { test, expect } from "bun:test"
import { extractEscalation } from "../src/escalate"

test("resposta sem marcador passa intacta", () => {
  const r = extractEscalation("oi, tudo bem?")
  expect(r.text).toBe("oi, tudo bem?")
  expect(r.reason).toBeNull()
})

test("extrai o motivo e limpa o marcador", () => {
  const r = extractEscalation("deixa eu confirmar e já te falo [[escalar: pergunta sobre preço do CREATE]]")
  expect(r.text).toBe("deixa eu confirmar e já te falo")
  expect(r.reason).toBe("pergunta sobre preço do CREATE")
})

test("marcador sem motivo", () => {
  const r = extractEscalation("já te respondo!\n[[escalar]]")
  expect(r.text).toBe("já te respondo!")
  expect(r.reason).toBe("")
})

test("marcador sozinho deixa texto vazio (silêncio)", () => {
  const r = extractEscalation("[[escalar: não entendi nada]]")
  expect(r.text).toBe("")
  expect(r.reason).toBe("não entendi nada")
})
