import { createClient } from 'redis'
import { env } from '@santos-tech/env'

export const redis = createClient({ url: env.REDIS_URL })

redis.on('error', (err) => console.error('Redis error:', err))

await redis.connect()
