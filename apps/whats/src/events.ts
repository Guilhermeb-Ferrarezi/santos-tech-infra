import { EventEmitter } from "events"
import type { ServerResponse } from "http"

// Bus de eventos em memória (o serviço roda em réplica única): wa.ts/http.ts emitem,
// o handler SSE retransmite pros dashboards conectados.

export interface WhatsEvent {
  type: "status" | "seen" | "allowlist" | "message"
  data?: unknown
}

const bus = new EventEmitter()
bus.setMaxListeners(200)

export function emitEvent(type: WhatsEvent["type"], data?: unknown): void {
  bus.emit("event", { type, ...(data === undefined ? {} : { data }) } satisfies WhatsEvent)
}

// sseFrame formata um evento no wire format do SSE. Pura — testável.
export function sseFrame(ev: WhatsEvent): string {
  return `data: ${JSON.stringify(ev)}\n\n`
}

// serveSSE prende a resposta aberta, retransmite o bus e manda heartbeat a cada 25s
// (mantém o Traefik/navegador de olho na conexão).
export function serveSSE(res: ServerResponse): void {
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  })
  res.write(": ok\n\n")

  const onEvent = (ev: WhatsEvent) => res.write(sseFrame(ev))
  bus.on("event", onEvent)
  const heartbeat = setInterval(() => res.write(": ping\n\n"), 25_000)

  res.on("close", () => {
    clearInterval(heartbeat)
    bus.off("event", onEvent)
  })
}
