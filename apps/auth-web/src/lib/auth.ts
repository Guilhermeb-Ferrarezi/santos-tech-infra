import { apiFetch } from './api'

export type User = {
  id: string
  email: string
  name: string
  createdAt: string
}

export type AuthResponse = { user: User }

export async function login(email: string, password: string): Promise<AuthResponse> {
  return apiFetch<AuthResponse>('/auth/login', {
    method: 'POST',
    body: JSON.stringify({ email, password }),
  })
}

export async function me(): Promise<AuthResponse> {
  return apiFetch<AuthResponse>('/auth/me')
}

export async function forgotPassword(email: string): Promise<void> {
  await apiFetch('/auth/forgot-password', {
    method: 'POST',
    body: JSON.stringify({ email }),
  })
}

export async function resetPassword(token: string, newPassword: string): Promise<void> {
  await apiFetch('/auth/reset-password', {
    method: 'POST',
    body: JSON.stringify({ token, newPassword }),
  })
}

export function getSafeRedirect(param: string | null | undefined): string {
  const fallback = import.meta.env.VITE_REDIRECT_DEFAULT ?? '/'
  if (!param) return fallback
  try {
    const url = new URL(param)
    const ok =
      url.hostname === 'santos-tech.com' ||
      url.hostname.endsWith('.santos-tech.com') ||
      url.hostname === 'localhost'
    return ok ? param : fallback
  } catch {
    return fallback
  }
}
