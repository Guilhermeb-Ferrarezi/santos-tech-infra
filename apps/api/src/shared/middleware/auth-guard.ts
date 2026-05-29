import fp from 'fastify-plugin'
import type { FastifyInstance, FastifyRequest, FastifyReply } from 'fastify'
import { verifyAccessToken } from '@/modules/auth/auth.service'
import { AppError } from '@/shared/errors/app-error'

declare module 'fastify' {
  interface FastifyInstance {
    authGuard: (req: FastifyRequest, reply: FastifyReply) => Promise<void>
  }
  interface FastifyRequest {
    userId: number
  }
}

export const authGuardPlugin = fp(async (app: FastifyInstance) => {
  app.decorate('authGuard', async (request: FastifyRequest, _reply: FastifyReply) => {
    const token = request.cookies.access_token
    if (!token) throw new AppError(401, 'UNAUTHORIZED', 'Não autenticado')

    const userId = await verifyAccessToken(token).catch(() => {
      throw new AppError(401, 'UNAUTHORIZED', 'Token inválido ou expirado')
    })

    request.userId = userId
  })
})
