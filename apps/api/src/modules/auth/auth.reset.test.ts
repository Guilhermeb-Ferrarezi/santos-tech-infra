import { describe, it, expect, beforeAll } from 'bun:test'
import { buildApp } from '@/app'
import { db } from '@/db/client'
import { users, passwordResets } from '@/db/schema'
import { eq } from 'drizzle-orm'
import { randomBytes, createHash } from 'crypto'
import type { FastifyInstance } from 'fastify'

let app: FastifyInstance

beforeAll(async () => {
  app = await buildApp()
})

describe('POST /auth/forgot-password', () => {
  it('retorna 200 para email existente e cria token no banco', async () => {
    const email = `forgot-${Date.now()}@santos-tech.com`
    await app.inject({
      method: 'POST',
      url: '/auth/register',
      payload: { email, name: 'Forgot Test', password: 'senha-segura-123' },
    })

    const res = await app.inject({
      method: 'POST',
      url: '/auth/forgot-password',
      payload: { email },
    })
    expect(res.statusCode).toBe(200)

    const [user] = await db.select().from(users).where(eq(users.email, email)).limit(1)
    const [reset] = await db
      .select()
      .from(passwordResets)
      .where(eq(passwordResets.userId, user.id))
      .limit(1)
    expect(reset).toBeDefined()
    expect(reset.expiresAt > new Date()).toBe(true)
  })

  it('retorna 200 mesmo para email inexistente', async () => {
    const res = await app.inject({
      method: 'POST',
      url: '/auth/forgot-password',
      payload: { email: 'naoexiste@santos-tech.com' },
    })
    expect(res.statusCode).toBe(200)
  })
})

describe('POST /auth/reset-password', () => {
  it('redefine senha com token válido e permite novo login', async () => {
    const email = `reset-${Date.now()}@santos-tech.com`
    await app.inject({
      method: 'POST',
      url: '/auth/register',
      payload: { email, name: 'Reset Test', password: 'senha-antiga-123' },
    })
    const [user] = await db.select().from(users).where(eq(users.email, email)).limit(1)

    const rawToken = randomBytes(32).toString('hex')
    const tokenHash = createHash('sha256').update(rawToken).digest('hex')
    await db.insert(passwordResets).values({
      userId: user.id,
      tokenHash,
      expiresAt: new Date(Date.now() + 60 * 60 * 1000),
    })

    const res = await app.inject({
      method: 'POST',
      url: '/auth/reset-password',
      payload: { token: rawToken, newPassword: 'nova-senha-segura-123' },
    })
    expect(res.statusCode).toBe(200)

    const loginRes = await app.inject({
      method: 'POST',
      url: '/auth/login',
      payload: { email, password: 'nova-senha-segura-123' },
    })
    expect(loginRes.statusCode).toBe(200)
  })

  it('retorna 400 para token inválido', async () => {
    const res = await app.inject({
      method: 'POST',
      url: '/auth/reset-password',
      payload: { token: 'token-invalido-qualquer', newPassword: 'nova-senha-123' },
    })
    expect(res.statusCode).toBe(400)
  })

  it('retorna 400 para token expirado', async () => {
    const email = `expired-${Date.now()}@santos-tech.com`
    await app.inject({
      method: 'POST',
      url: '/auth/register',
      payload: { email, name: 'Expired Test', password: 'senha-antiga-123' },
    })
    const [user] = await db.select().from(users).where(eq(users.email, email)).limit(1)

    const rawToken = randomBytes(32).toString('hex')
    const tokenHash = createHash('sha256').update(rawToken).digest('hex')
    await db.insert(passwordResets).values({
      userId: user.id,
      tokenHash,
      expiresAt: new Date(Date.now() - 1000), // expirado
    })

    const res = await app.inject({
      method: 'POST',
      url: '/auth/reset-password',
      payload: { token: rawToken, newPassword: 'nova-senha-123' },
    })
    expect(res.statusCode).toBe(400)
  })
})
