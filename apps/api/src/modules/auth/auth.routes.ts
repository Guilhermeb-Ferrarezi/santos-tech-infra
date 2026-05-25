import type { FastifyInstance } from 'fastify'
import {
  registerHandler,
  loginHandler,
  logoutHandler,
  meHandler,
  refreshHandler,
} from './auth.handler'
import { registerSchema, loginSchema } from './auth.schema'

export async function authRoutes(app: FastifyInstance) {
  app.post('/register', { schema: registerSchema }, registerHandler)
  app.post('/login', { schema: loginSchema }, loginHandler)
  app.post('/logout', logoutHandler)
  app.get('/me', meHandler)
  app.post('/refresh', refreshHandler)
}
