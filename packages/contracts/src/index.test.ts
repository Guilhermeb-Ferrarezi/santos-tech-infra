import { describe, it, expect } from 'bun:test'
import type { User, AuthResponse, ApiError } from './index'

describe('contracts types', () => {
  it('User tem campos obrigatórios', () => {
    const user: User = {
      id: '123',
      email: 'test@test.com',
      name: 'Test',
      createdAt: new Date().toISOString(),
    }
    expect(user.id).toBe('123')
  })

  it('ApiError tem code e message', () => {
    const err: ApiError = { code: 'UNAUTHORIZED', message: 'Não autorizado' }
    expect(err.code).toBe('UNAUTHORIZED')
  })
})
