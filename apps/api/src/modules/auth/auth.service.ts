import { createHash } from 'node:crypto'
import { SignJWT, jwtVerify } from 'jose'
import { env } from '@santos-tech/env'

const ACCESS_SECRET = new TextEncoder().encode(env.JWT_SECRET)
const REFRESH_SECRET = new TextEncoder().encode(env.JWT_REFRESH_SECRET)
const ACCESS_TTL = process.env.JWT_ACCESS_TTL ?? '2h'
const REFRESH_TTL = '7d'

export async function hashPassword(password: string): Promise<string> {
  return Bun.password.hash(password)
}

export async function verifyPassword(password: string, hash: string): Promise<boolean> {
  return Bun.password.verify(password, hash)
}

export async function generateTokens(userId: number, email?: string) {
  const payload: Record<string, unknown> = { sub: String(userId) }
  if (email) payload.email = email

  const accessToken = await new SignJWT(payload)
    .setProtectedHeader({ alg: 'HS256' })
    .setExpirationTime(ACCESS_TTL)
    .setIssuedAt()
    .sign(ACCESS_SECRET)

  const refreshToken = await new SignJWT({ sub: String(userId) })
    .setProtectedHeader({ alg: 'HS256' })
    .setExpirationTime(REFRESH_TTL)
    .setIssuedAt()
    .sign(REFRESH_SECRET)

  return { accessToken, refreshToken }
}

export async function verifyAccessToken(token: string): Promise<number> {
  const { payload } = await jwtVerify(token, ACCESS_SECRET)
  return Number(payload.sub)
}

export async function verifyRefreshToken(token: string): Promise<number> {
  const { payload } = await jwtVerify(token, REFRESH_SECRET)
  return Number(payload.sub)
}

export function hashRefreshToken(token: string): string {
  return createHash('sha256').update(token).digest('hex')
}

export function cookieOptions(env_: typeof env) {
  return {
    httpOnly: true,
    secure: env_.NODE_ENV === 'production',
    sameSite: 'lax' as const,
    domain: env_.COOKIE_DOMAIN,
    path: '/',
  }
}
