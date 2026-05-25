import { SignJWT, jwtVerify } from 'jose'
import { env } from '@santos-tech/env'

const ACCESS_SECRET = new TextEncoder().encode(env.JWT_SECRET)
const REFRESH_SECRET = new TextEncoder().encode(env.JWT_REFRESH_SECRET)
const ACCESS_TTL = '15m'
const REFRESH_TTL = '7d'

export async function hashPassword(password: string): Promise<string> {
  return Bun.password.hash(password)
}

export async function verifyPassword(password: string, hash: string): Promise<boolean> {
  return Bun.password.verify(password, hash)
}

export async function generateTokens(userId: string) {
  const accessToken = await new SignJWT({ sub: userId })
    .setProtectedHeader({ alg: 'HS256' })
    .setExpirationTime(ACCESS_TTL)
    .setIssuedAt()
    .sign(ACCESS_SECRET)

  const refreshToken = await new SignJWT({ sub: userId })
    .setProtectedHeader({ alg: 'HS256' })
    .setExpirationTime(REFRESH_TTL)
    .setIssuedAt()
    .sign(REFRESH_SECRET)

  return { accessToken, refreshToken }
}

export async function verifyAccessToken(token: string): Promise<string> {
  const { payload } = await jwtVerify(token, ACCESS_SECRET)
  return payload.sub as string
}

export async function verifyRefreshToken(token: string): Promise<string> {
  const { payload } = await jwtVerify(token, REFRESH_SECRET)
  return payload.sub as string
}

export function hashRefreshToken(token: string): string {
  return Bun.hash(token).toString(16)
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
