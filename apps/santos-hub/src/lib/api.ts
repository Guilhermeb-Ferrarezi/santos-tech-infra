import { fetch as nativeFetch } from "@tauri-apps/plugin-http";

export const API_ORIGIN = "https://api.santos-tech.com";

// Chamadas nativas (via tauri-plugin-http, execução em Rust — não passa pela
// engine da webview): o request NUNCA carrega header Origin, então o backend
// sempre trata como "cliente nativo" e devolve accessToken/refreshToken no
// corpo (ver Convenções do llms.txt) em vez de depender de cookie — evita de
// vez a dor de cabeça de SameSite/cross-site cookie numa origem tauri://.
export function apiFetch(path: string, init?: RequestInit) {
  return nativeFetch(`${API_ORIGIN}${path}`, init);
}

export class ApiError extends Error {
  status: number;
  code?: string;
  constructor(status: number, message: string, code?: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export async function apiJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await apiFetch(path, init);
  const text = await res.text();
  const body = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    throw new ApiError(res.status, body?.message ?? `HTTP ${res.status}`, body?.code);
  }
  return body as T;
}
