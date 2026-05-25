import { createHash, randomBytes } from 'crypto'
import { Resend } from 'resend'
import { env } from '@santos-tech/env'

const client = new Resend(env.RESEND_API_KEY)

export function generateResetToken(): { token: string; hash: string } {
  const token = randomBytes(32).toString('hex')
  const hash = createHash('sha256').update(token).digest('hex')
  return { token, hash }
}

export function hashResetToken(token: string): string {
  return createHash('sha256').update(token).digest('hex')
}

export async function sendPasswordResetEmail(to: string, resetUrl: string): Promise<void> {
  if (env.NODE_ENV === 'test') return
  const safeUrl = new URL(resetUrl).toString()
  try {
    await client.emails.send({
      from: env.RESEND_FROM_EMAIL,
      to,
      subject: 'Recuperação de senha — Santos Tech',
      html: `
        <p>Olá!</p>
        <p>Recebemos uma solicitação para redefinir a senha da sua conta Santos Tech.</p>
        <p><a href="${safeUrl}" style="color:#187ABF">Clique aqui para criar uma nova senha</a></p>
        <p>Este link expira em <strong>1 hora</strong>.</p>
        <p>Se você não solicitou a recuperação, ignore este email.</p>
      `,
    })
  } catch (err) {
    throw new Error(`Falha ao enviar email de recuperação: ${err instanceof Error ? err.message : String(err)}`)
  }
}
