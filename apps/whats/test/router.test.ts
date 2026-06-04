import { test, expect } from "bun:test"
import { decideMessage } from "../src/router"

const base = {
  jid: "5511@s.whatsapp.net",
  fromMe: false,
  ownerJID: "5513@s.whatsapp.net",
  text: "oi",
  hasMedia: false,
  allowlist: new Set(["5511@s.whatsapp.net"]),
  autoReplyEnabled: true,
}

test("ignora chat fora da allowlist", () => {
  const d = decideMessage({ ...base, jid: "9999@s.whatsapp.net" })
  expect(d.action).toBe("ignore")
})

test("ignora quando auto-reply desligado", () => {
  expect(decideMessage({ ...base, autoReplyEnabled: false }).action).toBe("ignore")
})

test("ignora mensagem própria que não é o chat do dono", () => {
  expect(decideMessage({ ...base, fromMe: true }).action).toBe("ignore")
})

test("responde com mídia avisando incapacidade", () => {
  const d = decideMessage({ ...base, hasMedia: true, text: "" })
  expect(d.action).toBe("reply_static")
  if (d.action === "reply_static") expect(d.text.length).toBeGreaterThan(0)
})

test("chat permitido com texto vira turno sem ferramentas", () => {
  const d = decideMessage(base)
  expect(d.action).toBe("run_turn")
  if (d.action === "run_turn") {
    expect(d.toolsDisabled).toBe(true)
    expect(d.text).toBe("oi")
  }
})

test("chat do dono roda turno COM ferramentas", () => {
  const d = decideMessage({ ...base, jid: base.ownerJID, allowlist: new Set([base.ownerJID]) })
  expect(d.action).toBe("run_turn")
  if (d.action === "run_turn") expect(d.toolsDisabled).toBe(false)
})
