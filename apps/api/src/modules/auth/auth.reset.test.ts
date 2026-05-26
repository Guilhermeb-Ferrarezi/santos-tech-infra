import { describe, it, expect, beforeAll } from 'bun:test'
import { buildApp } from '@/app'
import { db } from '@/db/client'
import { users } from '@/db/schema'
import { redis } from '@/db/redis'
import { eq } from 'drizzle-orm'
import { generateResetToken, hashResetToken } from '@/shared/email/resend'
import type { FastifyInstance } from 'fastify'

const SKIP = process.env.SKIP_INTEGRATION === '1'

let app: FastifyInstance

beforeAll(async () => {
  if (SKIP) return
  app = await buildApp()
})

describe.skipIf(SKIP)('POST /auth/forgot-password', () => {
  it('retorna 200 para email existente', async () => {
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

describe.skipIf(SKIP)('POST /auth/reset-password', () => {
  it('redefine senha com token válido e permite novo login', async () => {
    const email = `reset-${Date.now()}@santos-tech.com`
    await app.inject({
      method: 'POST',
      url: '/auth/register',
      payload: { email, name: 'Reset Test', password: 'senha-antiga-123' },
    })
    const [user] = await db.select().from(users).where(eq(users.email, email)).limit(1)

    const { token, hash } = generateResetToken()
    await redis.set(`pwd_reset:${hash}`, user.id, { EX: 3600 })

    const res = await app.inject({
      method: 'POST',
      url: '/auth/reset-password',
      payload: { token, newPassword: 'nova-senha-segura-123' },
    })
    expect(res.statusCode).toBe(200)

    const loginRes = await app.inject({
      method: 'POST',
      url: '/auth/login',
      payload: { identifier: email, password: 'nova-senha-segura-123' },
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

  it('retorna 400 para token expirado (não existe no Redis)', async () => {
    const { token, hash } = generateResetToken()
    await redis.set(`pwd_reset:${hash}`, 'user-id-fake', { EX: 1 })
    await redis.del(`pwd_reset:${hash}`)

    const res = await app.inject({
      method: 'POST',
      url: '/auth/reset-password',
      payload: { token, newPassword: 'nova-senha-123' },
    })
    expect(res.statusCode).toBe(400)
  })
})
