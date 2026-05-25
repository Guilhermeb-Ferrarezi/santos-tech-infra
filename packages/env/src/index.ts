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

let env: Env | undefined

export function getEnv(): Env {
  if (!env) {
    env = createEnv()
  }
  return env
}

// Only attempt to initialize env in non-test environments
// In tests, use createEnv() directly or getEnv() after setting up process.env
if (process.env.NODE_ENV !== 'test') {
  try {
    env = createEnv()
  } catch (error) {
    // In production/development, this should fail loudly
    if (process.env.NODE_ENV === 'production') {
      throw error
    }
  }
}
