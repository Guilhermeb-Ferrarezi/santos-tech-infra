import { MongoClient } from 'mongodb'
import { env } from '@santos-tech/env'

const client = new MongoClient(env.MONGO_URL)

export async function getLogsCollection() {
  await client.connect()
  return client.db().collection('request_logs')
}

export { client as mongoClient }
