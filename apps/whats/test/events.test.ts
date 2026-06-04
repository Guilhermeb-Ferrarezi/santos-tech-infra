import { test, expect } from "bun:test"
import { sseFrame } from "../src/events"

test("formata um frame SSE com o evento serializado", () => {
  expect(sseFrame({ type: "message", data: { jid: "x@s.whatsapp.net" } })).toBe(
    'data: {"type":"message","data":{"jid":"x@s.whatsapp.net"}}\n\n',
  )
})

test("frame sem data", () => {
  expect(sseFrame({ type: "status" })).toBe('data: {"type":"status"}\n\n')
})
