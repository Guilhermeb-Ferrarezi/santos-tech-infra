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
