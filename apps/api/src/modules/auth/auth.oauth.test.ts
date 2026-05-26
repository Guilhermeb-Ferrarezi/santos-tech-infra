import { describe, it, expect, beforeAll } from 'bun:test'
import { buildApp } from '@/app'
import type { FastifyInstance } from 'fastify'

const SKIP = process.env.SKIP_INTEGRATION === '1'

let app: FastifyInstance

beforeAll(async () => {
  if (SKIP) return
  app = await buildApp()
})

describe.skipIf(SKIP)('GET /auth/google', () => {
  it('redireciona para o Google', async () => {
    const res = await app.inject({ method: 'GET', url: '/auth/google' })
    expect(res.statusCode).toBe(302)
    expect(res.headers.location).toContain('accounts.google.com')
  })
})

describe.skipIf(SKIP)('GET /auth/google/callback — com erro', () => {
  it('redireciona de volta ao frontend quando OAuth é negado', async () => {
    const res = await app.inject({
      method: 'GET',
      url: '/auth/google/callback?error=access_denied',
    })
    expect(res.statusCode).toBe(302)
    expect(res.headers.location).toContain('error=oauth_denied')
  })
})
