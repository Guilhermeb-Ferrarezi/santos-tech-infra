import { config } from "./config"
import { serviceToken } from "./jwt"

// Envia email PELA CAIXA DA CONTA DO CLAUDE (claude@santos-tech.com): o
// /mailbox/me/send resolve o remetente pela sessão (JWT assinado com o sub da
// conta do Claude) — o From é forçado pelo servidor, nunca pelo cliente.
export async function sendEmail(to: string, subject: string, text: string): Promise<void> {
  const res = await fetch(`${config.mailsAPI}/mailbox/me/send`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${serviceToken(config.claudeUserID)}`,
    },
    body: JSON.stringify({ to, subject, text }),
    signal: AbortSignal.timeout(20_000),
  })
  if (!res.ok) throw new Error(`envio de email falhou: HTTP ${res.status}`)
}
