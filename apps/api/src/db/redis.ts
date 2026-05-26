import { createClient } from 'redis'
import { env } from '@santos-tech/env'

const client = createClient({ url: env.REDIS_URL })

client.on('error', (err) => console.error('Redis error:', err))

let connected = false

async function connect() {
  if (!connected) {
    await client.connect()
    connected = true
  }
}

export const redis = {
  get: async (key: string) => { await connect(); return client.get(key) },
  set: async (key: string, value: string, options?: { EX?: number }) => {
    await connect()
    return options?.EX ? client.set(key, value, { EX: options.EX }) : client.set(key, value)
  },
  del: async (key: string) => { await connect(); return client.del(key) },
}
