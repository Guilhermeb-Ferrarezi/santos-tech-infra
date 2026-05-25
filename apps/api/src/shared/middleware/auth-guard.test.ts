import { describe, it, expect, beforeAll } from 'bun:test'
import { buildApp } from '@/app'
import type { FastifyInstance } from 'fastify'

let app: FastifyInstance

beforeAll(async () => {
  app = await buildApp()
  app.get('/protected', { preHandler: [app.authGuard] }, async (_req, reply) => {
    reply.send({ ok: true })
  })
})

describe('authGuard', () => {
  it('retorna 401 sem cookie', async () => {
    const res = await app.inject({ method: 'GET', url: '/protected' })
    expect(res.statusCode).toBe(401)
  })
})
