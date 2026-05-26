import { MongoClient } from 'mongodb'
import { env } from '@santos-tech/env'

const client = new MongoClient(env.MONGO_URL, {
  maxPoolSize: 3,
  minPoolSize: 1,
  serverSelectionTimeoutMS: 5000,
})

let connected = false

export async function getLogsCollection() {
  if (!connected) {
    await client.connect()
    connected = true
  }
  return client.db().collection('request_logs')
}

export { client as mongoClient }
