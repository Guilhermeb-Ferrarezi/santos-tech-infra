export type User = {
  id: string
  email: string
  name: string
  createdAt: string
}

export type AuthResponse = {
  user: User
}

export type ApiError = {
  code: string
  message: string
}

export type RegisterBody = {
  email: string
  name: string
  password: string
}

export type LoginBody = {
  email: string
  password: string
}
