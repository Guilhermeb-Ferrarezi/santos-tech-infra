import { test, expect } from "bun:test"
import { finalTextFromEvents } from "../src/agent"

test("pega o texto do evento result", () => {
  const events = [
    { type: "init" },
    { type: "delta", text: "oi" },
    { type: "delta", text: " tudo bem" },
    { type: "result", data: { result: "oi tudo bem?" } },
    { type: "done" },
  ]
  expect(finalTextFromEvents(events)).toBe("oi tudo bem?")
})

test("cai pros deltas concatenados quando result não traz texto", () => {
  const events = [
    { type: "delta", text: "parte 1 " },
    { type: "delta", text: "parte 2" },
    { type: "done" },
  ]
  expect(finalTextFromEvents(events)).toBe("parte 1 parte 2")
})
