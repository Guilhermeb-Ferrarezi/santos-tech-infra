import Redis from "ioredis"
import { config } from "./config"

export const redis = new Redis(config.redisURL)

const AUTO_KEY = "whats:autoreply"

export async function autoReplyEnabled(): Promise<boolean> {
  return (await redis.get(AUTO_KEY)) === "1"
}

export async function setAutoReply(on: boolean): Promise<void> {
  await redis.set(AUTO_KEY, on ? "1" : "0")
}

// initAutoReply garante default DESLIGADO no primeiro boot (segurança).
export async function initAutoReply(): Promise<void> {
  if ((await redis.get(AUTO_KEY)) === null) await redis.set(AUTO_KEY, "0")
}

// ── Simulador: transcript e typing por admin ─────────────────────────────────
// O simulador é uma "bancada" do pipeline real: as respostas que iriam pro
// WhatsApp caem aqui (ver deliver/presence no wa.ts).

export interface SimMessage {
  role: "user" | "bot"
  text: string
  ts: number
}

const SIM_TTL = 7 * 24 * 3600 // 7 dias
const simKey = (uid: number) => `whats:sim:${uid}`
const simTypingKey = (uid: number) => `whats:sim:typing:${uid}`

export async function pushSim(uid: number, role: "user" | "bot", text: string): Promise<void> {
  const key = simKey(uid)
  await redis
    .multi()
    .rpush(key, JSON.stringify({ role, text, ts: Date.now() } satisfies SimMessage))
    .ltrim(key, -100, -1)
    .expire(key, SIM_TTL)
    .exec()
}

export async function listSim(uid: number): Promise<SimMessage[]> {
  const raw = await redis.lrange(simKey(uid), 0, -1)
  return raw.map((r) => JSON.parse(r) as SimMessage)
}

export async function clearSim(uid: number): Promise<void> {
  await redis.del(simKey(uid), simTypingKey(uid))
}

export async function setSimTyping(uid: number, on: boolean): Promise<void> {
  if (on) await redis.set(simTypingKey(uid), "1", "EX", 180)
  else await redis.del(simTypingKey(uid))
}

export async function simTyping(uid: number): Promise<boolean> {
  return (await redis.get(simTypingKey(uid))) === "1"
}
