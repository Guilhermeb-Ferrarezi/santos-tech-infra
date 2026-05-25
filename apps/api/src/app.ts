import Fastify from 'fastify'
import cookie from '@fastify/cookie'
import cors from '@fastify/cors'
import rateLimit from '@fastify/rate-limit'
import { env } from '@santos-tech/env'
import { errorHandler } from '@/shared/errors/error-handler'
import { logRequest } from '@/shared/logger/mongo-transport'

export async function buildApp() {
  const app = Fastify({ logger: true })

  await app.register(cors, {
    origin: env.CORS_ORIGIN,
    credentials: true,
  })

  await app.register(cookie, {
    secret: env.JWT_SECRET,
  })

  await app.register(rateLimit, {
    max: 100,
    timeWindow: '1 minute',
  })

  app.addHook('onResponse', async (request, reply) => {
    await logRequest({
      method: request.method,
      path: request.routeOptions?.url ?? request.url,
      status: reply.statusCode,
      userId: (request as any).userId ?? null,
      ip: request.ip,
      durationMs: Math.round(reply.elapsedTime),
    })
  })

  app.setErrorHandler(errorHandler)

  // Auth routes registered in Task 8 — placeholder for now
  app.get('/health', async () => ({ ok: true }))

  return app
}

if (import.meta.main) {
  const app = await buildApp()
  await app.listen({ port: env.PORT, host: '0.0.0.0' })
  console.log(`API rodando na porta ${env.PORT}`)
}
