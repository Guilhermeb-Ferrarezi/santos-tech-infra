import { describe, it, expect } from 'bun:test'
import { AppError } from './app-error'

describe('AppError', () => {
  it('cria erro com status e code', () => {
    const err = new AppError(401, 'UNAUTHORIZED', 'Não autorizado')
    expect(err.statusCode).toBe(401)
    expect(err.code).toBe('UNAUTHORIZED')
    expect(err.message).toBe('Não autorizado')
  })

  it('é instância de Error', () => {
    expect(new AppError(400, 'BAD_REQUEST', 'teste')).toBeInstanceOf(Error)
  })
})
