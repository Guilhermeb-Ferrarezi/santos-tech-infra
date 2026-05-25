import { describe, it, expect, beforeAll } from 'bun:test'
import { buildApp } from '@/app'
import type { FastifyInstance } from 'fastify'

let app: FastifyInstance

beforeAll(async () => { app = await buildApp() })

describe('GET /auth/google', () => {
  it('redireciona para o Google', async () => {
    const res = await app.inject({ method: 'GET', url: '/auth/google' })
    expect(res.statusCode).toBe(302)
    expect(res.headers.location).toContain('accounts.google.com')
  })
})

describe('GET /auth/google/callback — com erro', () => {
  it('retorna 401 quando OAuth é negado', async () => {
    const res = await app.inject({
      method: 'GET',
      url: '/auth/google/callback?error=access_denied',
    })
    expect([400, 401]).toContain(res.statusCode)
  })
})
