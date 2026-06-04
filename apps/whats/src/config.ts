// Env do whats-agent. Obrigatórias falham no boot (igual mustEnv do Go).
function must(key: string): string {
  const v = process.env[key]
  if (!v) {
    console.error(`variável de ambiente obrigatória ausente: ${key}`)
    process.exit(1)
  }
  return v
}

export const config = {
  port: Number(process.env.PORT ?? "8080"),
  basePath: (process.env.BASE_PATH ?? "/whats").replace(/\/$/, ""),
  databaseURL: must("DATABASE_URL"),
  redisURL: must("REDIS_URL"),
  jwtSecret: must("JWT_SECRET"),
  agentURL: (process.env.AGENT_URL ?? "https://api.santos-tech.com/claude").replace(/\/$/, ""),
  ownerJID: must("OWNER_JID"), // ex: "5513999999999@s.whatsapp.net"
  ownerUserID: Number(must("OWNER_USER_ID")), // id da conta admin no auth central (sub do JWT)
  authURL: (process.env.AUTH_URL ?? "https://api.santos-tech.com").replace(/\/$/, ""),
  corsOrigins: (process.env.ALLOWED_ORIGINS ?? "").split(",").map((s) => s.trim()).filter(Boolean),
  sessionDir: process.env.SESSION_DIR ?? "/data/baileys",
  debounceMs: Number(process.env.DEBOUNCE_MS ?? "8000"),
}
