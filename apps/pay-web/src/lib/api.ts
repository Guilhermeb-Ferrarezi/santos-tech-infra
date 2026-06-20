const BASE = import.meta.env.VITE_API_URL ?? "https://api.santos-tech.com/payments";
const AUTH = import.meta.env.VITE_AUTH_URL ?? "https://auth.santos-tech.com";
// raiz da API do ecossistema (onde vive /auth/refresh), derivada do BASE.
const API_ROOT = BASE.replace(/\/payments\/?$/, "");

function redirectToLogin(): never {
  const back = encodeURIComponent(window.location.href);
  window.location.href = `${AUTH}/login?redirect=${back}`;
  throw new Error("redirecting");
}

async function req<T>(path: string, init?: RequestInit, retried = false): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    credentials: "include",
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  });
  if (res.status === 401) {
    // tenta renovar a sessão uma vez antes de mandar pro login (evita re-login à toa)
    if (!retried) {
      const refreshed = await fetch(`${API_ROOT}/auth/refresh`, { method: "POST", credentials: "include" });
      if (refreshed.ok) return req<T>(path, init, true);
    }
    redirectToLogin();
  }
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).message ?? `Erro ${res.status}`);
  return res.status === 204 ? (undefined as T) : res.json();
}

export interface Product {
  id: number;
  slug: string;
  name: string;
  description: string;
  priceCents: number;
  // Campos de assinatura (PIX Automático). `recurring` sempre presente; `periodicity`/`dueDay`
  // só vêm preenchidos em produtos recorrentes (omitempty no backend).
  recurring?: boolean;
  periodicity?: string;
  dueDay?: number | null;
}
export interface CartLine { product: Product; quantity: number; }
export interface PayData { amountCents: number; brCode: string; qrCode: string; status: string; dueDate: string; }

// seg codifica um segmento de path controlado pelo usuário (evita path/URL injection).
const seg = (v: string | number) => encodeURIComponent(String(v));

export const api = {
  product: (slug: string) => req<Product>(`/products/by-slug/${seg(slug)}`),
  cart: () => req<CartLine[]>(`/me/cart`),
  addToCart: (slug: string) => req<{ ok: boolean }>(`/me/cart`, { method: "POST", body: JSON.stringify({ slug }) }),
  removeFromCart: (productId: number) => req<{ ok: boolean }>(`/me/cart/${seg(Number(productId))}`, { method: "DELETE" }),
  checkout: (taxId: string, phone: string, name: string, email: string, save: boolean) =>
    req<{ token: string; brCode: string; qrCode: string; amountCents: number }>(`/me/cart/checkout`,
      { method: "POST", body: JSON.stringify({ taxId, phone, name, email, save }) }),
  // subscribe — checkout de produto recorrente (item único, fora do carrinho). Gera a
  // recorrência + a 1ª cobrança no mesmo QR de autorização. Mesma forma do checkout.
  subscribe: (productId: number, taxId: string, phone: string, name: string, email: string, save: boolean) =>
    req<{ token: string; brCode: string; qrCode: string; amountCents: number }>(`/me/subscribe`,
      { method: "POST", body: JSON.stringify({ productId, taxId, phone, name, email, save }) }),
  pay: (token: string) => req<PayData>(`/pay/${seg(token)}`),
  payEventsUrl: (token: string) => `${BASE}/pay/${seg(token)}/events`,
  cancelPay: (token: string) => req<{ status: string }>(`/pay/${seg(token)}/cancel`, { method: "POST" }),
  history: () => req<unknown[]>(`/me/charges`),
};

/** Baixa o comprovante PDF da cobrança paga pelo token público (pagador, sem login). */
export async function downloadPayReceipt(token: string): Promise<void> {
  const res = await fetch(`${BASE}/pay/${seg(token)}/receipt`, { credentials: "include" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "comprovante.pdf";
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}
export { BASE };
