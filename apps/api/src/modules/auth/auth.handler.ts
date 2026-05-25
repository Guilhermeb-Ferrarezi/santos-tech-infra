import type { FastifyRequest, FastifyReply } from 'fastify'
import { eq } from 'drizzle-orm'
import { db } from '@/db/client'
import { users, sessions, passwordResets } from '@/db/schema'
import { env } from '@santos-tech/env'
import { AppError } from '@/shared/errors/app-error'
import {
  hashPassword,
  verifyPassword,
  generateTokens,
  verifyAccessToken,
  verifyRefreshToken,
  hashRefreshToken,
  cookieOptions,
} from './auth.service'
import {
  generateResetToken,
  hashResetToken,
  sendPasswordResetEmail,
} from '@/shared/email/resend'

type RegisterBody = { email: string; name: string; password: string }
type LoginBody = { email: string; password: string }

export async function registerHandler(
  request: FastifyRequest<{ Body: RegisterBody }>,
  reply: FastifyReply,
) {
  const { email, name, password } = request.body

  const existing = await db.select().from(users).where(eq(users.email, email)).limit(1)
  if (existing.length > 0) {
    throw new AppError(409, 'EMAIL_ALREADY_EXISTS', 'Este email já está cadastrado')
  }

  const passwordHash = await hashPassword(password)
  const [user] = await db.insert(users).values({ email, name, passwordHash }).returning()

  return reply.status(201).send({
    user: { id: user.id, email: user.email, name: user.name, createdAt: user.createdAt.toISOString() },
  })
}

export async function loginHandler(
  request: FastifyRequest<{ Body: LoginBody }>,
  reply: FastifyReply,
) {
  const { email, password } = request.body

  const [user] = await db.select().from(users).where(eq(users.email, email)).limit(1)
  if (!user || !user.passwordHash) {
    throw new AppError(401, 'INVALID_CREDENTIALS', 'Email ou senha inválidos')
  }

  const valid = await verifyPassword(password, user.passwordHash)
  if (!valid) {
    throw new AppError(401, 'INVALID_CREDENTIALS', 'Email ou senha inválidos')
  }

  const { accessToken, refreshToken } = await generateTokens(user.id)

  const expiresAt = new Date()
  expiresAt.setDate(expiresAt.getDate() + 7)
  await db.insert(sessions).values({
    userId: user.id,
    refreshTokenHash: hashRefreshToken(refreshToken),
    expiresAt,
  })

  const opts = cookieOptions(env)
  reply
    .setCookie('access_token', accessToken, { ...opts, maxAge: 15 * 60 })
    .setCookie('refresh_token', refreshToken, { ...opts, maxAge: 7 * 24 * 60 * 60 })

  return reply.send({
    user: { id: user.id, email: user.email, name: user.name, createdAt: user.createdAt.toISOString() },
  })
}

export async function logoutHandler(_request: FastifyRequest, reply: FastifyReply) {
  const opts = cookieOptions(env)
  reply
    .clearCookie('access_token', opts)
    .clearCookie('refresh_token', opts)
  return reply.status(204).send()
}

export async function meHandler(request: FastifyRequest, reply: FastifyReply) {
  const token = request.cookies.access_token
  if (!token) throw new AppError(401, 'UNAUTHORIZED', 'Não autenticado')

  const userId = await verifyAccessToken(token).catch(() => {
    throw new AppError(401, 'UNAUTHORIZED', 'Token inválido ou expirado')
  })

  const [user] = await db.select().from(users).where(eq(users.id, userId)).limit(1)
  if (!user) throw new AppError(401, 'UNAUTHORIZED', 'Usuário não encontrado')

  ;(request as any).userId = user.id

  return reply.send({
    user: { id: user.id, email: user.email, name: user.name, createdAt: user.createdAt.toISOString() },
  })
}

export async function refreshHandler(request: FastifyRequest, reply: FastifyReply) {
  const token = request.cookies.refresh_token
  if (!token) throw new AppError(401, 'UNAUTHORIZED', 'Refresh token ausente')

  const userId = await verifyRefreshToken(token).catch(() => {
    throw new AppError(401, 'UNAUTHORIZED', 'Refresh token inválido')
  })

  const tokenHash = hashRefreshToken(token)
  const [session] = await db
    .select()
    .from(sessions)
    .where(eq(sessions.refreshTokenHash, tokenHash))
    .limit(1)

  if (!session || session.expiresAt < new Date()) {
    throw new AppError(401, 'UNAUTHORIZED', 'Sessão expirada')
  }

  const { accessToken, refreshToken: newRefresh } = await generateTokens(userId)

  const expiresAt = new Date()
  expiresAt.setDate(expiresAt.getDate() + 7)

  await db.delete(sessions).where(eq(sessions.id, session.id))
  await db.insert(sessions).values({
    userId,
    refreshTokenHash: hashRefreshToken(newRefresh),
    expiresAt,
  })

  const opts = cookieOptions(env)
  reply
    .setCookie('access_token', accessToken, { ...opts, maxAge: 15 * 60 })
    .setCookie('refresh_token', newRefresh, { ...opts, maxAge: 7 * 24 * 60 * 60 })

  return reply.status(204).send()
}

export async function forgotPasswordHandler(
  request: FastifyRequest<{ Body: { email: string } }>,
  reply: FastifyReply,
) {
  const { email } = request.body

  const [user] = await db.select().from(users).where(eq(users.email, email)).limit(1)

  if (user) {
    await db.delete(passwordResets).where(eq(passwordResets.userId, user.id))

    const { token, hash } = generateResetToken()
    const expiresAt = new Date(Date.now() + 60 * 60 * 1000)

    await db.insert(passwordResets).values({ userId: user.id, tokenHash: hash, expiresAt })

    const resetUrl = `${env.CORS_ORIGIN}/reset-password?token=${token}`
    await sendPasswordResetEmail(email, resetUrl)
  }

  return reply.status(200).send({ message: 'ok' })
}

export async function resetPasswordHandler(
  request: FastifyRequest<{ Body: { token: string; newPassword: string } }>,
  reply: FastifyReply,
) {
  const { token, newPassword } = request.body

  const tokenHash = hashResetToken(token)
  const [reset] = await db
    .select()
    .from(passwordResets)
    .where(eq(passwordResets.tokenHash, tokenHash))
    .limit(1)

  if (!reset || reset.expiresAt < new Date()) {
    throw new AppError(400, 'INVALID_TOKEN', 'Link de recuperação inválido ou expirado')
  }

  await db
    .update(users)
    .set({ passwordHash: await hashPassword(newPassword) })
    .where(eq(users.id, reset.userId))

  await db.delete(sessions).where(eq(sessions.userId, reset.userId))
  await db.delete(passwordResets).where(eq(passwordResets.id, reset.id))

  return reply.status(200).send({ message: 'ok' })
}
