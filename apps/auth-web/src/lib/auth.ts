import { apiFetch } from './api'

export type User = {
  id: string
  email: string
  name: string
  createdAt: string
  avatarUrl?: string | null
  username?: string | null
  mfaEnabled?: boolean
  mfaMethod?: 'totp' | 'email'
  mfaTotp?: boolean
  mfaEmail?: boolean
}

export type AuthResponse = { user: User }

// Login com MFA ativo não emite sessão: devolve um desafio pro 2º passo.
// method = o preferido do usuário (mostrado primeiro); methods = os disponíveis.
export type MfaChallenge = {
  mfaRequired: true
  challenge: string
  method?: 'totp' | 'email'
  methods?: string[]
}
export type LoginResult = AuthResponse | MfaChallenge

export async function login(identifier: string, password: string): Promise<LoginResult> {
  return apiFetch<LoginResult>('/auth/login', {
    method: 'POST',
    body: JSON.stringify({ identifier, password }),
  })
}

// 2º passo do MFA: valida o código (TOTP, código por email ou de recuperação) e emite a sessão.
export async function verifyMfa(challenge: string, code: string): Promise<AuthResponse> {
  return apiFetch<AuthResponse>('/auth/mfa/verify', {
    method: 'POST',
    body: JSON.stringify({ challenge, code }),
  })
}

// Envia um código de verificação por email (alternativa ao app autenticador).
export async function sendMfaEmail(challenge: string): Promise<void> {
  await apiFetch('/auth/mfa/email', {
    method: 'POST',
    body: JSON.stringify({ challenge }),
  })
}

export async function me(): Promise<AuthResponse> {
  return apiFetch<AuthResponse>('/auth/me')
}

export async function forgotPassword(email: string): Promise<void> {
  await apiFetch('/auth/forgot-password', {
    method: 'POST',
    body: JSON.stringify({ email }),
  })
}

export async function resetPassword(token: string, newPassword: string): Promise<void> {
  await apiFetch('/auth/reset-password', {
    method: 'POST',
    body: JSON.stringify({ token, newPassword }),
  })
}

// Sudo mode: confirma a identidade (MFA ou senha) e eleva a sessão por 15min.
export async function sudoVerify(payload: { code?: string; password?: string }): Promise<void> {
  await apiFetch('/auth/sudo/verify', {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

// Envia um código pro email da CONTA logada (usado no sudo e na gestão do 2FA).
export async function sendAccountEmailCode(): Promise<void> {
  await apiFetch('/auth/mfa/email-code', { method: 'POST' })
}

export function getSafeRedirect(param: string | null | undefined): string {
  const fallback = import.meta.env.VITE_REDIRECT_DEFAULT ?? '/'
  if (!param) return fallback
  try {
    const url = new URL(param)
    // Exige https (http só em dev) pra evitar downgrade; localhost só em dev.
    const schemeOK = url.protocol === 'https:' || (import.meta.env.DEV && url.protocol === 'http:')
    const hostOK =
      url.hostname === 'santos-tech.com' ||
      url.hostname.endsWith('.santos-tech.com') ||
      (import.meta.env.DEV && url.hostname === 'localhost')
    return schemeOK && hostOK ? param : fallback
  } catch {
    return fallback
  }
}
