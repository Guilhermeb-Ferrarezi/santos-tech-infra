import { describe, it, expect } from 'bun:test'
import { hashPassword, verifyPassword, generateTokens, verifyAccessToken } from './auth.service'

describe('hashPassword / verifyPassword', () => {
  it('verifica senha correta', async () => {
    const hash = await hashPassword('minha-senha-123')
    expect(await verifyPassword('minha-senha-123', hash)).toBe(true)
  })

  it('rejeita senha errada', async () => {
    const hash = await hashPassword('minha-senha-123')
    expect(await verifyPassword('senha-errada', hash)).toBe(false)
  })
})

describe('generateTokens / verifyAccessToken', () => {
  it('gera tokens válidos', async () => {
    const { accessToken, refreshToken } = await generateTokens('user-id-123')
    expect(accessToken).toBeTypeOf('string')
    expect(refreshToken).toBeTypeOf('string')
  })

  it('verifica access token e retorna userId', async () => {
    const { accessToken } = await generateTokens('user-id-abc')
    const userId = await verifyAccessToken(accessToken)
    expect(userId).toBe('user-id-abc')
  })

  it('rejeita token inválido', async () => {
    await expect(verifyAccessToken('token-invalido')).rejects.toThrow()
  })
})
