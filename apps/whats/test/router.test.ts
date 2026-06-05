import { test, expect } from "bun:test"
import { decideMessage } from "../src/router"

const base = {
  jid: "5511@s.whatsapp.net",
  fromMe: false,
  ownerJID: "5513@s.whatsapp.net",
  text: "oi",
  media: null,
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

test("vídeo mantém resposta estática", () => {
  const d = decideMessage({ ...base, text: "", media: { kind: "video", claudeReadable: false, oversize: false } })
  expect(d.action).toBe("reply_static")
  if (d.action === "reply_static") expect(d.text.length).toBeGreaterThan(0)
})

test("áudio mantém resposta estática", () => {
  const d = decideMessage({ ...base, text: "", media: { kind: "audio", claudeReadable: false, oversize: false } })
  expect(d.action).toBe("reply_static")
})

test("imagem em chat permitido vira turno com anexo", () => {
  const d = decideMessage({ ...base, text: "olha isso", media: { kind: "image", claudeReadable: true, oversize: false } })
  expect(d.action).toBe("run_turn")
  if (d.action === "run_turn") {
    expect(d.attach).toBe(true)
    expect(d.toolsDisabled).toBe(true)
  }
})

test("pdf em chat permitido vira turno com anexo", () => {
  const d = decideMessage({ ...base, text: "", media: { kind: "pdf", claudeReadable: true, oversize: false } })
  expect(d.action).toBe("run_turn")
  if (d.action === "run_turn") expect(d.attach).toBe(true)
})

test("imagem grande demais responde estático", () => {
  const d = decideMessage({ ...base, text: "", media: { kind: "image", claudeReadable: true, oversize: true } })
  expect(d.action).toBe("reply_static")
})

test("chat permitido com texto vira turno sem ferramentas", () => {
  const d = decideMessage(base)
  expect(d.action).toBe("run_turn")
  if (d.action === "run_turn") {
    expect(d.toolsDisabled).toBe(true)
    expect(d.text).toBe("oi")
    expect(d.attach).toBe(false)
  }
})

test("chat do dono roda turno COM ferramentas", () => {
  const d = decideMessage({ ...base, jid: base.ownerJID, allowlist: new Set([base.ownerJID]) })
  expect(d.action).toBe("run_turn")
  if (d.action === "run_turn") expect(d.toolsDisabled).toBe(false)
})
