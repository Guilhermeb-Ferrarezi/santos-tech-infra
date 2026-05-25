export const registerSchema = {
  body: {
    type: 'object',
    required: ['email', 'name', 'password'],
    properties: {
      email: { type: 'string', format: 'email' },
      name: { type: 'string', minLength: 2 },
      password: { type: 'string', minLength: 8 },
    },
  },
} as const

export const loginSchema = {
  body: {
    type: 'object',
    required: ['email', 'password'],
    properties: {
      email: { type: 'string', format: 'email' },
      password: { type: 'string', minLength: 1 },
    },
  },
} as const

export const forgotPasswordSchema = {
  body: {
    type: 'object',
    required: ['email'],
    properties: {
      email: { type: 'string', format: 'email' },
    },
  },
} as const

export const resetPasswordSchema = {
  body: {
    type: 'object',
    required: ['token', 'newPassword'],
    properties: {
      token: { type: 'string', minLength: 1 },
      newPassword: { type: 'string', minLength: 8 },
    },
  },
} as const
