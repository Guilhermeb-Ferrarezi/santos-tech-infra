import { config } from "./config"

// Cliente da API de emails do ecossistema (mails.santos-tech.com). O /send é
// protegido por X-Api-Key (env do serviço) — padrão dos apps, ver CLAUDE.md.
export async function sendEmail(to: string, subject: string, text: string): Promise<void> {
  if (!config.emailApiKey) throw new Error("EMAIL_API_KEY ausente")
  const res = await fetch(`${config.mailsAPI}/send`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Api-Key": config.emailApiKey },
    body: JSON.stringify({ to, subject, text }),
    signal: AbortSignal.timeout(20_000),
  })
  if (!res.ok) throw new Error(`envio de email falhou: HTTP ${res.status}`)
}
