import { getLogsCollection } from '@/db/mongo'

type LogEntry = {
  method: string
  path: string
  status: number
  userId: string | null
  ip: string
  durationMs: number
  timestamp: Date
}

type LogInput = Omit<LogEntry, 'timestamp'>

export function buildLogEntry(input: LogInput): LogEntry {
  return { ...input, timestamp: new Date() }
}

export async function logRequest(input: LogInput): Promise<void> {
  try {
    const collection = await getLogsCollection()
    await collection.insertOne(buildLogEntry(input))
  } catch {
    // log failures não devem quebrar a API
  }
}
