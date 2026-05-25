import { describe, it, expect, beforeAll } from 'bun:test'
import { buildApp } from '@/app'
import type { FastifyInstance } from 'fastify'

let app: FastifyInstance

beforeAll(async () => {
  app = await buildApp()
})

describe('POST /auth/register', () => {
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

describe('POST /auth/login', () => {
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
      payload: { email, password: 'minha-senha-123' },
    })
    expect(res.statusCode).toBe(200)
    expect(res.headers['set-cookie']).toBeDefined()
  })

  it('retorna 401 para senha errada', async () => {
    const res = await app.inject({
      method: 'POST',
      url: '/auth/login',
      payload: { email: 'naoexiste@test.com', password: 'senha-errada' },
    })
    expect(res.statusCode).toBe(401)
  })
})
