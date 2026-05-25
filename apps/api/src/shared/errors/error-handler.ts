import type { FastifyError, FastifyReply, FastifyRequest } from 'fastify'
import { AppError } from './app-error'

export function errorHandler(
  error: FastifyError | AppError | Error,
  request: FastifyRequest,
  reply: FastifyReply,
) {
  if (error instanceof AppError) {
    return reply.status(error.statusCode).send({
      code: error.code,
      message: error.message,
    })
  }

  if ('statusCode' in error && error.statusCode === 400) {
    return reply.status(400).send({
      code: 'VALIDATION_ERROR',
      message: error.message,
    })
  }

  request.log.error(error)
  return reply.status(500).send({
    code: 'INTERNAL_ERROR',
    message: 'Erro interno do servidor',
  })
}
