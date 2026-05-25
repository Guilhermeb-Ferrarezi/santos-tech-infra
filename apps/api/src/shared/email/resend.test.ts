import { describe, it, expect } from 'bun:test'
import { generateResetToken } from './resend'

describe('generateResetToken', () => {
  it('retorna token e hash distintos', () => {
    const { token, hash } = generateResetToken()
    expect(token).toBeString()
    expect(hash).toBeString()
    expect(token).not.toBe(hash)
    expect(token.length).toBe(64) // 32 bytes em hex
    expect(hash.length).toBe(64) // sha256 em hex
  })

  it('gera tokens diferentes a cada chamada', () => {
    const a = generateResetToken()
    const b = generateResetToken()
    expect(a.token).not.toBe(b.token)
    expect(a.hash).not.toBe(b.hash)
  })

  it('mesmo token sempre produz mesmo hash', () => {
    const { token, hash } = generateResetToken()
    const { createHash } = require('crypto')
    const expectedHash = createHash('sha256').update(token).digest('hex')
    expect(hash).toBe(expectedHash)
  })
})
