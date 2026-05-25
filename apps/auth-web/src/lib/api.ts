export class ApiError extends Error {
  constructor(
    public readonly code: string,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new ApiError(data?.code ?? 'UNKNOWN', data?.message ?? 'Erro desconhecido')
  }
  return data as T
}
