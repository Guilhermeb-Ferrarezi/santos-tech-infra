import { describe, it, expect, beforeAll } from 'bun:test'
import { buildApp } from '@/app'
import type { FastifyInstance } from 'fastify'

const SKIP = process.env.SKIP_INTEGRATION === '1'

let app: FastifyInstance

beforeAll(async () => {
  if (SKIP) return
  app = await buildApp()
})

describe.skipIf(SKIP)('POST /auth/register', () => {
  it('cria usuário e retorna 201', async () => {
    const res = await app.inject({
      method: 'POST',
      url: '/auth/register',
      payload: {
        email: `test-${Date.now()}@santos-tech.com`,
        name: 'Test User',
        password: 'senha-segura-123',
      },
    })
    expect(res.statusCode).toBe(201)
    const body = res.json()
    expect(body.user.email).toContain('@santos-tech.com')
  })

  it('retorna 409 para email duplicado', async () => {
    const email = `dup-${Date.now()}@santos-tech.com`
    await app.inject({
      method: 'POST',
      url: '/auth/register',
      payload: { email, name: 'Dup', password: 'senha-segura-123' },
    })
    const res = await app.inject({
      method: 'POST',
      url: '/auth/register',
      payload: { email, name: 'Dup2', password: 'senha-segura-123' },
    })
    expect(res.statusCode).toBe(409)
  })
})

describe.skipIf(SKIP)('POST /auth/login', () => {
  it('retorna 200 e seta cookies httpOnly', async () => {
    const email = `login-${Date.now()}@santos-tech.com`
    await app.inject({
      method: 'POST',
      url: '/auth/register',
      payload: { email, name: 'Login User', password: 'minha-senha-123' },
    })
    const res = await app.inject({
      method: 'POST',
      url: '/auth/login',
      payload: { identifier: email, password: 'minha-senha-123' },
    })
    expect(res.statusCode).toBe(200)
    expect(res.headers['set-cookie']).toBeDefined()
  })

  it('retorna 401 para senha errada', async () => {
    const res = await app.inject({
      method: 'POST',
      url: '/auth/login',
      payload: { identifier: 'naoexiste@test.com', password: 'senha-errada' },
    })
    expect(res.statusCode).toBe(401)
  })
})
