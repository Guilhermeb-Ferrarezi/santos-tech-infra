import { describe, it, expect } from 'bun:test'
import { createEnv } from './index'

describe('createEnv', () => {
  it('retorna variáveis quando todas estão presentes', () => {
    const vars = {
      DATABASE_URL: 'postgresql://localhost/test',
      MONGO_URL: 'mongodb://localhost/test',
      JWT_SECRET: 'secret-com-mais-de-16-chars',
      JWT_REFRESH_SECRET: 'refresh-secret-com-mais-de-16-chars',
      GOOGLE_CLIENT_ID: 'id',
      GOOGLE_CLIENT_SECRET: 'secret',
      GOOGLE_CALLBACK_URL: 'http://localhost/callback',
      PORT: '3000',
      COOKIE_DOMAIN: 'localhost',
      CORS_ORIGIN: 'http://localhost:5173',
      NODE_ENV: 'test',
      RESEND_API_KEY: 'test-resend-api-key',
      RESEND_FROM_EMAIL: 'noreply@santos-tech.com',
      REDIS_URL: 'redis://localhost:6379',
    }

    const env = createEnv(vars)
    expect(env.DATABASE_URL).toBe('postgresql://localhost/test')
    expect(env.PORT).toBe(3000)
  })

  it('lança erro quando variável obrigatória está ausente', () => {
    expect(() => createEnv({})).toThrow()
  })
})
