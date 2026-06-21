const BASE = import.meta.env.VITE_API_URL ?? "https://api.santos-tech.com/payments";
const AUTH = import.meta.env.VITE_AUTH_URL ?? "https://auth.santos-tech.com";

// Hardening: em produção a API DEVE ser HTTPS — senão o payment_token e o CPF do
// titular trafegariam em texto claro. Falha explícita no boot se mal configurado.
if (import.meta.env.PROD && !BASE.startsWith("https://")) {
  throw new Error("VITE_API_URL deve usar HTTPS em produção");
}
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
export interface PayData {
  amountCents: number;
  method?: string; // "pix" | "boleto" | "card" (ausente em cobranças antigas = pix)
  brCode: string; // pix: copia-e-cola · boleto: linha digitável
  qrCode: string;
  pdfUrl?: string; // boleto: link do PDF
  barcode?: string; // boleto: código de barras
  status: string;
  dueDate: string;
}

// Parcela de cartão de crédito retornada pelo endpoint GET /installments
export interface InstallmentOption {
  installments: number;
  value: number;  // centavos por parcela
  total: number;  // centavos total com juros
  label: string;  // ex.: "3× de R$ 10,00 (sem juros)"
}

// Payload para pagamento com cartão.
// NUNCA inclui PAN, CVV ou validade — esses dados ficam no browser e vão à Efí via efi.ts.
export interface CardPayPayload {
  paymentToken: string;       // token opaco gerado pela Efí no browser
  installments: number;
  holder: string;             // nome do titular
  holderDocument: string;     // CPF (só dígitos)
  billingAddress?: {
    zipCode: string;          // CEP (só dígitos)
    number: string;
    complement?: string;
  };
}

// seg codifica um segmento de path controlado pelo usuário (evita path/URL injection).
const seg = (v: string | number) => encodeURIComponent(String(v));

export interface ApplyCouponResult {
  valid: boolean;
  reason?: string;
  code?: string;
  discountType?: string;
  discountValue?: number;
  discountCents?: number;
  finalCents?: number;
}

export const api = {
  applyCoupon: (code: string, amountCents: number) =>
    req<ApplyCouponResult>(`/coupons/apply`, {
      method: "POST",
      body: JSON.stringify({ code, amountCents }),
    }),
  product: (slug: string) => req<Product>(`/products/by-slug/${seg(slug)}`),
  cart: () => req<CartLine[]>(`/me/cart`),
  addToCart: (slug: string) => req<{ ok: boolean }>(`/me/cart`, { method: "POST", body: JSON.stringify({ slug }) }),
  removeFromCart: (productId: number) => req<{ ok: boolean }>(`/me/cart/${seg(Number(productId))}`, { method: "DELETE" }),
  checkout: (taxId: string, phone: string, name: string, email: string, save: boolean, coupon?: string) =>
    req<{ token: string; brCode: string; qrCode: string; amountCents: number }>(`/me/cart/checkout`,
      { method: "POST", body: JSON.stringify({ taxId, phone, name, email, save, ...(coupon ? { coupon } : {}) }) }),
  // subscribe — checkout de produto recorrente (item único, fora do carrinho). Cria a
  // recorrência (PIX Automático, Jornada 2) e devolve o QR de AUTORIZAÇÃO. A 1ª cobrança
  // vem depois (no dia do vencimento ou logo após aprovar, conforme o produto).
  subscribe: (productId: number, taxId: string, phone: string, name: string, email: string, save: boolean) =>
    req<{ token: string; brCode: string; qrCode: string; amountCents: number }>(`/me/subscribe`,
      { method: "POST", body: JSON.stringify({ productId, taxId, phone, name, email, save }) }),
  // subscribeStatus / subscribeEventsUrl — acompanham a autorização da assinatura pelo token.
  subscribeStatus: (token: string) =>
    req<{ status: string; brCode: string; qrCode: string; amountCents: number }>(`/subscribe/${seg(token)}`),
  subscribeEventsUrl: (token: string) => `${BASE}/subscribe/${seg(token)}/events`,
  pay: (token: string) => req<PayData>(`/pay/${seg(token)}`),
  payEventsUrl: (token: string) => `${BASE}/pay/${seg(token)}/events`,
  cancelPay: (token: string) => req<{ status: string }>(`/pay/${seg(token)}/cancel`, { method: "POST" }),
  history: () => req<unknown[]>(`/me/charges`),

  // --- Cartão de crédito ---

  // GET /installments?brand=&total= — opções de parcelamento para a bandeira e valor.
  // Retorna array de InstallmentOption. Seguro para trafegar (não há dado sensível).
  getInstallments: (brand: string, totalCents: number) =>
    req<InstallmentOption[]>(`/installments?brand=${encodeURIComponent(brand)}&total=${encodeURIComponent(totalCents)}`),

  // POST /link/{token}/pay — efetua o pagamento com cartão.
  // NUNCA inclua PAN/CVV/validade no payload — apenas o payment_token (opaco) e metadados.
  payCard: (token: string, payload: CardPayPayload, coupon?: string) =>
    req<{ status: string }>(`/link/${seg(token)}/pay`, {
      method: "POST",
      body: JSON.stringify({ method: "card", ...payload, ...(coupon ? { coupon } : {}) }),
    }),
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
