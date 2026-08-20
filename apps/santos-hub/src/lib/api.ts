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

// PAT do Hub, embarcado no build (VITE_SANTOS_HUB_TOKEN). GET /public/downloads
// não pede login de usuário, mas exige este header desde que o catálogo passou
// a listar instaladores .exe/.msi — ver santosHubGuard em api-go
// (handlers_downloads.go). Sem o header a rota responde 401; o valor tem que
// ser o MESMO da env SANTOS_HUB_TOKEN do api-go.
//
// Só vai nas rotas do catálogo: mandar o PAT em toda chamada o espalharia por
// requests que não têm nada a ver com ele (login, /auth/me).
export function hubHeaders(): Record<string, string> {
  const token = import.meta.env.VITE_SANTOS_HUB_TOKEN;
  return token ? { "X-Santos-Hub-Token": token } : {};
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
