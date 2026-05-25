import { describe, it, expect } from 'bun:test'
import { buildLogEntry } from './mongo-transport'

describe('buildLogEntry', () => {
  it('cria entry com campos obrigatórios', () => {
    const entry = buildLogEntry({
      method: 'POST',
      path: '/auth/login',
      status: 200,
      userId: null,
      ip: '127.0.0.1',
      durationMs: 42,
    })
    expect(entry.method).toBe('POST')
    expect(entry.path).toBe('/auth/login')
    expect(entry.timestamp).toBeInstanceOf(Date)
  })
})
