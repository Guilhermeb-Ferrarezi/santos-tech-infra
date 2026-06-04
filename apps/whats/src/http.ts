import { createServer, IncomingMessage, ServerResponse } from "http"
import jwt from "jsonwebtoken"
import QRCode from "qrcode"
import { config } from "./config"
import { listAllowlist, addAllow, removeAllow, listSeen, listMessages, userRole, unlinkChat } from "./db"
import { waStatus, logoutWhatsApp, injectSimMessage, simJID } from "./wa"
import { autoReplyEnabled, setAutoReply, listSim, clearSim, simTyping } from "./redis"
import { serveSSE, emitEvent } from "./events"

const ROLE_ADMIN = 3

function send(res: ServerResponse, status: number, body: unknown) {
  res.writeHead(status, { "Content-Type": "application/json" })
  res.end(JSON.stringify(body))
}

function cors(req: IncomingMessage, res: ServerResponse) {
  const origin = req.headers.origin ?? ""
  if (config.corsOrigins.includes(origin)) {
    res.setHeader("Access-Control-Allow-Origin", origin)
    res.setHeader("Access-Control-Allow-Credentials", "true")
    res.setHeader("Vary", "Origin")
  }
  res.setHeader("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
  res.setHeader("Access-Control-Allow-Headers", "Content-Type")
}

// adminUserID valida o JWT do auth central (cookie access_token ou Bearer) e exige o
// papel admin consultando a tabela users — o JWT do ecossistema não carrega claim de
// role. Devolve o userID (sub) ou null (fail-closed).
async function adminUserID(req: IncomingMessage): Promise<number | null> {
  let token = ""
  const cookie = req.headers.cookie ?? ""
  const m = /(?:^|;\s*)access_token=([^;]+)/.exec(cookie)
  if (m) token = decodeURIComponent(m[1]!)
  else if (req.headers.authorization?.startsWith("Bearer ")) token = req.headers.authorization.slice(7)
  if (!token) return null
  try {
    const claims = jwt.verify(token, config.jwtSecret, { algorithms: ["HS256"] }) as { sub?: string }
    const uid = Number(claims.sub)
    if (!Number.isInteger(uid) || uid <= 0) return null
    return (await userRole(uid)) === ROLE_ADMIN ? uid : null
  } catch {
    return null
  }
}

async function body(req: IncomingMessage): Promise<any> {
  const chunks: Buffer[] = []
  for await (const c of req) chunks.push(c as Buffer)
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {}
}

export function startHTTP() {
  const b = config.basePath
  const server = createServer(async (req, res) => {
    try {
      cors(req, res)
      if (req.method === "OPTIONS") return send(res, 204, {})
      const url = (req.url ?? "").split("?")[0] ?? ""

      if (req.method === "GET" && url === `${b}/health`) return send(res, 200, { service: "whats", status: "ok" })

      const uid = await adminUserID(req)
      if (uid === null) return send(res, 401, { code: "UNAUTHORIZED", message: "sessão inválida" })

      // Tempo real: o dashboard ouve este stream e invalida as queries por tipo.
      if (req.method === "GET" && url === `${b}/events`) return serveSSE(res)
      if (req.method === "GET" && url === `${b}/messages`) {
        const jid = new URL(req.url ?? "", "http://x").searchParams.get("jid") ?? ""
        if (!jid) return send(res, 400, { code: "BAD_REQUEST", message: "jid obrigatório" })
        return send(res, 200, { items: await listMessages(jid) })
      }

      if (req.method === "GET" && url === `${b}/status`) {
        const s = waStatus()
        return send(res, 200, { status: s.status, number: s.number, autoReply: await autoReplyEnabled() })
      }
      if (req.method === "GET" && url === `${b}/qr`) {
        const s = waStatus()
        if (!s.qr) return send(res, 200, { qr: null })
        return send(res, 200, { qr: await QRCode.toDataURL(s.qr) })
      }
      if (req.method === "POST" && url === `${b}/toggle`) {
        const { enabled } = await body(req)
        await setAutoReply(!!enabled)
        return send(res, 200, { autoReply: !!enabled })
      }
      if (req.method === "POST" && url === `${b}/disconnect`) {
        await logoutWhatsApp()
        return send(res, 200, { ok: true })
      }
      // Simulador: bancada do pipeline real (ver spec do dashboard)
      if (req.method === "GET" && url === `${b}/sim`) {
        return send(res, 200, { messages: await listSim(uid), typing: await simTyping(uid) })
      }
      if (req.method === "POST" && url === `${b}/sim/send`) {
        const { text } = await body(req)
        if (!text || typeof text !== "string" || !text.trim())
          return send(res, 400, { code: "BAD_REQUEST", message: "text obrigatório" })
        await injectSimMessage(uid, text.trim())
        return send(res, 202, { ok: true })
      }
      if (req.method === "POST" && url === `${b}/sim/reset`) {
        await clearSim(uid)
        await unlinkChat(simJID(uid))
        return send(res, 200, { ok: true })
      }

      if (req.method === "GET" && url === `${b}/seen`) return send(res, 200, { items: await listSeen() })
      if (req.method === "GET" && url === `${b}/allowlist`) return send(res, 200, { items: await listAllowlist() })
      if (req.method === "POST" && url === `${b}/allowlist`) {
        const { jid, label } = await body(req)
        if (!jid || typeof jid !== "string") return send(res, 400, { code: "BAD_REQUEST", message: "jid obrigatório" })
        await addAllow(jid, typeof label === "string" ? label : "")
        emitEvent("allowlist")
        return send(res, 201, { ok: true })
      }
      if (req.method === "DELETE" && url.startsWith(`${b}/allowlist/`)) {
        const jid = decodeURIComponent(url.slice(`${b}/allowlist/`.length))
        await removeAllow(jid)
        emitEvent("allowlist")
        return send(res, 200, { ok: true })
      }
      return send(res, 404, { code: "NOT_FOUND", message: "rota inexistente" })
    } catch (e) {
      console.error("erro na requisição", req.method, req.url, e)
      return send(res, 500, { code: "INTERNAL", message: "erro interno" })
    }
  })
  server.listen(config.port, () => console.log(`whats ouvindo :${config.port}${b}`))
}
