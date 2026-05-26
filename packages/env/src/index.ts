import { z } from 'zod'

const schema = z.object({
  DATABASE_URL: z.string().min(1),
  MONGO_URL: z.string().min(1),
  JWT_SECRET: z.string().min(16),
  JWT_REFRESH_SECRET: z.string().min(16),
  GOOGLE_CLIENT_ID: z.string().min(1),
  GOOGLE_CLIENT_SECRET: z.string().min(1),
  GOOGLE_CALLBACK_URL: z.string().url(),
  PORT: z.coerce.number().default(3000),
  COOKIE_DOMAIN: z.string().min(1),
  CORS_ORIGIN: z.string().min(1),
  NODE_ENV: z.enum(['development', 'production', 'test']).default('development'),
  RESEND_API_KEY: z.string().min(1),
  RESEND_FROM_EMAIL: z.string().email(),
  REDIS_URL: z.string().min(1),
})

export type Env = z.infer<typeof schema>

export function createEnv(input: Record<string, unknown> = process.env): Env {
  const result = schema.safeParse(input)
  if (!result.success) {
    const missing = result.error.issues.map(i => i.path.join('.')).join(', ')
    throw new Error(`Variáveis de ambiente inválidas ou ausentes: ${missing}`)
  }
  return result.data
}

export const env: Env = createEnv()
