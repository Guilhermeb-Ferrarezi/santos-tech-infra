# Checkout Frontends (pay-web + dashboard admin) — Plano de Implementação (Plano 2 de 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. **Depende do Plano 1 (backend) estar implementado** — a API de `payments-go` precisa expor os endpoints de produto/carrinho/checkout/SSE.

**Goal:** Construir a tela de checkout do cliente (`apps/pay-web`, novo) e as páginas de admin de produtos/cobranças no `org/dashboard`.

**Architecture:** `pay-web` é um SPA React 19 + Vite + Tailwind + shadcn/ui, público, que usa a sessão compartilhada `.santos-tech.com` (cookie `access_token`) e consome `api.santos-tech.com/payments`. O admin reusa o `dashboard-web` (já React + shadcn) adicionando páginas que consomem os endpoints admin.

**Tech Stack:** React 19, Vite, Tailwind, shadcn/ui, `EventSource` (SSE), `fetch` com `credentials:'include'`.

---

## Parte A — `apps/pay-web` (novo)

**Base URL da API:** `https://api.santos-tech.com/payments` (prod) / configurável por `VITE_API_URL`.
**Auth:** cookie `access_token` (Domain `.santos-tech.com`) já enviado em requests para `api.santos-tech.com` com `credentials:'include'`. Em `401`, redireciona para `https://auth.santos-tech.com/login?redirect=<url-atual>`.

### Estrutura de arquivos (`apps/pay-web/`)
| Arquivo | Responsabilidade |
|---------|------------------|
| `package.json`, `vite.config.ts`, `tsconfig.json`, `index.html` | scaffold Vite + React + TS |
| `tailwind.config.js`, `src/index.css` | Tailwind + paleta Santos Tech + tokens shadcn |
| `src/components/ui/*` | shadcn: button, card, input, label, checkbox, skeleton |
| `src/lib/api.ts` | fetch tipado + redirect de auth |
| `src/lib/format.ts` | `formatBRL(cents)` |
| `src/main.tsx`, `src/App.tsx` | router |
| `src/pages/Product.tsx` | `/p/:slug` |
| `src/pages/Cart.tsx` | `/cart` |
| `src/pages/Checkout.tsx` | `/pay/:token` (QR + SSE) |
| `src/pages/History.tsx` | `/historico` |
| `src/components/PixView.tsx`, `CopyField.tsx`, `StatusBadge.tsx`, `SuccessScreen.tsx`, `PayerForm.tsx` | UI |

### Task A1: Scaffold Vite + React + Tailwind + shadcn

- [ ] **Step 1: criar o app**

Run:
```bash
cd /home/guilherme/projetos/sg/santos-tech-infra/apps
bun create vite pay-web --template react-ts
cd pay-web && bun install
bun add react-router-dom
bun add -d tailwindcss @tailwindcss/vite
```

- [ ] **Step 2: Tailwind v4 via plugin Vite** — `vite.config.ts`:

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  base: "/",
  plugins: [react(), tailwindcss()],
});
```

`src/index.css`:
```css
@import "tailwindcss";

:root {
  --st-blue: #187abf;
  --st-teal: #0db88f;
  --st-navy: #0e2937;
}
body { background: #f5f8fa; color: #212121; font-family: system-ui, sans-serif; }
```

- [ ] **Step 3: shadcn** — copie os componentes `button`, `card`, `input`, `label`, `checkbox`, `skeleton` do `org/dashboard/web/src/components/ui/` para `apps/pay-web/src/components/ui/` (mesmos arquivos; ajuste o import util `cn`). Crie `src/lib/utils.ts`:

```ts
import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
```
Run: `cd apps/pay-web && bun add clsx tailwind-merge class-variance-authority lucide-react`

- [ ] **Step 4: build**

Run: `cd apps/pay-web && bun run build`
Expected: build de produção sem erros de tipo.

- [ ] **Step 5: commit**

```bash
git add apps/pay-web
git commit -m "feat(pay-web): scaffold Vite + React + Tailwind + shadcn"
```

### Task A2: lib/api + formatação

- [ ] **Step 1: `src/lib/format.ts`**

```ts
export function formatBRL(cents: number): string {
  return (cents / 100).toLocaleString("pt-BR", { style: "currency", currency: "BRL" });
}
```

- [ ] **Step 2: `src/lib/api.ts`**

```ts
const BASE = import.meta.env.VITE_API_URL ?? "https://api.santos-tech.com/payments";
const AUTH = import.meta.env.VITE_AUTH_URL ?? "https://auth.santos-tech.com";

function redirectToLogin(): never {
  const back = encodeURIComponent(window.location.href);
  window.location.href = `${AUTH}/login?redirect=${back}`;
  throw new Error("redirecting");
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, { credentials: "include", ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) } });
  if (res.status === 401) redirectToLogin();
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).message ?? `Erro ${res.status}`);
  return res.status === 204 ? (undefined as T) : res.json();
}

export interface Product { id: number; slug: string; name: string; description: string; priceCents: number; }
export interface CartLine { product: Product; quantity: number; }
export interface PayData { amountCents: number; brCode: string; qrCode: string; status: string; dueDate: string; }

export const api = {
  product: (slug: string) => req<Product>(`/products/by-slug/${slug}`),
  cart: () => req<CartLine[]>(`/me/cart`),
  addToCart: (slug: string) => req<{ ok: boolean }>(`/me/cart`, { method: "POST", body: JSON.stringify({ slug }) }),
  removeFromCart: (productId: number) => req<{ ok: boolean }>(`/me/cart/${productId}`, { method: "DELETE" }),
  checkout: (taxId: string, phone: string, save: boolean) =>
    req<{ token: string; brCode: string; qrCode: string; amountCents: number }>(`/me/cart/checkout`,
      { method: "POST", body: JSON.stringify({ taxId, phone, save }) }),
  pay: (token: string) => req<PayData>(`/pay/${token}`),
  history: () => req<any[]>(`/me/charges`),
};
export { BASE };
```

- [ ] **Step 3: build + commit**

Run: `cd apps/pay-web && bun run build`
```bash
git add apps/pay-web && git commit -m "feat(pay-web): client da API + formatação BRL"
```

### Task A3: Componentes de UI (PixView, CopyField, StatusBadge, SuccessScreen, PayerForm)

- [ ] **Step 1: `src/components/CopyField.tsx`**

```tsx
import { useState } from "react";
import { Button } from "./ui/button";

export function CopyField({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex gap-2 items-stretch">
      <code className="flex-1 break-all rounded-lg bg-slate-100 p-3 text-xs">{value}</code>
      <Button onClick={() => { navigator.clipboard.writeText(value); setCopied(true); setTimeout(() => setCopied(false), 2000); }}>
        {copied ? "Copiado!" : "Copiar"}
      </Button>
    </div>
  );
}
```

- [ ] **Step 2: `src/components/PayerForm.tsx`** — CPF/telefone + checkbox shadcn "salvar"

```tsx
import { useState } from "react";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Checkbox } from "./ui/checkbox";
import { Button } from "./ui/button";

export function PayerForm({ onSubmit }: { onSubmit: (taxId: string, phone: string, save: boolean) => void }) {
  const [taxId, setTaxId] = useState("");
  const [phone, setPhone] = useState("");
  const [save, setSave] = useState(true);
  return (
    <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); onSubmit(taxId, phone, save); }}>
      <div className="space-y-1">
        <Label htmlFor="cpf">CPF</Label>
        <Input id="cpf" value={taxId} onChange={(e) => setTaxId(e.target.value)} placeholder="000.000.000-00" required />
      </div>
      <div className="space-y-1">
        <Label htmlFor="tel">Telefone</Label>
        <Input id="tel" value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="(16) 90000-0000" />
      </div>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox checked={save} onCheckedChange={(v) => setSave(Boolean(v))} />
        Salvar meus dados para as próximas compras
      </label>
      <Button type="submit" className="w-full">Gerar Pix</Button>
    </form>
  );
}
```

- [ ] **Step 3: `StatusBadge.tsx` + `SuccessScreen.tsx`**

```tsx
// StatusBadge.tsx
export function StatusBadge({ status }: { status: string }) {
  const map: Record<string, string> = {
    pending: "bg-amber-100 text-amber-800", paid: "bg-emerald-100 text-emerald-800",
    expired: "bg-rose-100 text-rose-800",
  };
  const label: Record<string, string> = { pending: "Aguardando pagamento", paid: "Pago", expired: "Expirado" };
  return <span className={`rounded-full px-3 py-1 text-xs font-medium ${map[status] ?? "bg-slate-100"}`}>{label[status] ?? status}</span>;
}
```
```tsx
// SuccessScreen.tsx
export function SuccessScreen() {
  return (
    <div className="text-center py-12">
      <div className="mx-auto mb-4 grid h-16 w-16 place-items-center rounded-full bg-emerald-100 text-3xl">✅</div>
      <h2 className="text-xl font-bold text-[#0e2937]">Pagamento confirmado!</h2>
      <p className="text-slate-600 mt-2">Obrigado. Você já pode fechar esta página.</p>
    </div>
  );
}
```

- [ ] **Step 4: build + commit**

Run: `cd apps/pay-web && bun run build`
```bash
git add apps/pay-web && git commit -m "feat(pay-web): componentes de checkout (CopyField, PayerForm, StatusBadge, Success)"
```

### Task A4: Páginas + router + SSE

- [ ] **Step 1: `src/pages/Product.tsx`** — `/p/:slug`

```tsx
import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api, type Product } from "../lib/api";
import { formatBRL } from "../lib/format";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";

export default function ProductPage() {
  const { slug = "" } = useParams();
  const nav = useNavigate();
  const [p, setP] = useState<Product | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => { api.product(slug).then(setP).catch(() => setErr("Produto não encontrado")); }, [slug]);
  if (err) return <Centered>{err}</Centered>;
  if (!p) return <Centered>Carregando…</Centered>;
  return (
    <Centered>
      <Card className="p-6 max-w-md w-full space-y-4">
        <h1 className="text-2xl font-bold text-[#0e2937]">{p.name}</h1>
        <p className="text-slate-600">{p.description}</p>
        <div className="text-3xl font-bold text-[#0db88f]">{formatBRL(p.priceCents)}</div>
        <Button className="w-full" onClick={async () => { await api.addToCart(slug); nav("/cart"); }}>
          Comprar
        </Button>
      </Card>
    </Centered>
  );
}
export function Centered({ children }: { children: React.ReactNode }) {
  return <div className="min-h-screen grid place-items-center p-4">{children}</div>;
}
```

- [ ] **Step 2: `src/pages/Cart.tsx`** — `/cart`, lista itens e dispara checkout

```tsx
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, type CartLine } from "../lib/api";
import { formatBRL } from "../lib/format";
import { Card } from "../components/ui/card";
import { PayerForm } from "../components/PayerForm";
import { Centered } from "./Product";

export default function CartPage() {
  const nav = useNavigate();
  const [lines, setLines] = useState<CartLine[]>([]);
  useEffect(() => { api.cart().then(setLines).catch(() => {}); }, []);
  const total = lines.reduce((s, l) => s + l.product.priceCents * l.quantity, 0);
  return (
    <Centered>
      <Card className="p-6 max-w-md w-full space-y-4">
        <h1 className="text-xl font-bold text-[#0e2937]">Sua conta</h1>
        {lines.map((l) => (
          <div key={l.product.id} className="flex justify-between text-sm">
            <span>{l.product.name} ×{l.quantity}</span><span>{formatBRL(l.product.priceCents * l.quantity)}</span>
          </div>
        ))}
        <div className="flex justify-between font-bold border-t pt-2">
          <span>Total</span><span>{formatBRL(total)}</span>
        </div>
        <PayerForm onSubmit={async (taxId, phone, save) => {
          const r = await api.checkout(taxId, phone, save);
          nav(`/pay/${r.token}`);
        }} />
      </Card>
    </Centered>
  );
}
```

- [ ] **Step 3: `src/pages/Checkout.tsx`** — `/pay/:token`, QR + **SSE**

```tsx
import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { api, type PayData, BASE } from "../lib/api";
import { formatBRL } from "../lib/format";
import { Card } from "../components/ui/card";
import { CopyField } from "../components/CopyField";
import { StatusBadge } from "../components/StatusBadge";
import { SuccessScreen } from "../components/SuccessScreen";
import { Centered } from "./Product";

export default function CheckoutPage() {
  const { token = "" } = useParams();
  const [data, setData] = useState<PayData | null>(null);
  const [paid, setPaid] = useState(false);

  useEffect(() => { api.pay(token).then((d) => { setData(d); setPaid(d.status === "paid"); }).catch(() => {}); }, [token]);

  useEffect(() => {
    if (paid) return;
    const es = new EventSource(`${BASE}/pay/${token}/events`, { withCredentials: true });
    es.addEventListener("paid", () => { setPaid(true); es.close(); });
    return () => es.close();
  }, [token, paid]);

  if (paid) return <Centered><Card className="p-6 max-w-md w-full"><SuccessScreen /></Card></Centered>;
  if (!data) return <Centered>Carregando…</Centered>;
  return (
    <Centered>
      <Card className="p-6 max-w-md w-full space-y-4 text-center">
        <StatusBadge status={data.status} />
        <div className="text-3xl font-bold text-[#0db88f]">{formatBRL(data.amountCents)}</div>
        {data.qrCode && <img src={data.qrCode} alt="QR Code Pix" className="mx-auto h-56 w-56" />}
        <p className="text-sm text-slate-600">Pague com Pix copia-e-cola:</p>
        <CopyField value={data.brCode} />
        <p className="text-xs text-slate-400">A confirmação aparece aqui automaticamente.</p>
      </Card>
    </Centered>
  );
}
```

- [ ] **Step 4: `src/App.tsx` + `src/main.tsx`** — router

```tsx
// App.tsx
import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import ProductPage from "./pages/Product";
import CartPage from "./pages/Cart";
import CheckoutPage from "./pages/Checkout";

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/p/:slug" element={<ProductPage />} />
        <Route path="/cart" element={<CartPage />} />
        <Route path="/pay/:token" element={<CheckoutPage />} />
        <Route path="*" element={<Navigate to="/cart" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
```
```tsx
// main.tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import App from "./App";
createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
```

- [ ] **Step 5: build + commit**

Run: `cd apps/pay-web && bun run build`
Expected: build OK.
```bash
git add apps/pay-web && git commit -m "feat(pay-web): páginas de produto, carrinho e checkout com SSE"
```

### Task A5: Dockerfile + serviço Coolify (deploy estático)

- [ ] **Step 1: `infra/Dockerfile.pay-web`** (espelha o `Dockerfile.auth-web` — confira o real e replique o servidor estático usado lá; abaixo, nginx)

```dockerfile
FROM oven/bun:1 AS build
WORKDIR /src
COPY apps/pay-web/package.json apps/pay-web/bun.lock ./
RUN bun install --frozen-lockfile
COPY apps/pay-web/ ./
RUN bun run build

FROM nginx:alpine
COPY --from=build /src/dist /usr/share/nginx/html
# SPA fallback
RUN printf 'server { listen 80; root /usr/share/nginx/html; location / { try_files $uri /index.html; } }' > /etc/nginx/conf.d/default.conf
EXPOSE 80
```

- [ ] **Step 2: app na Coolify** — criar via API (mesmo método do payments-go): build_pack `dockerfile`, `dockerfile_location:/infra/Dockerfile.pay-web`, branch da feature, domínio `https://pagar.santos-tech.com`, env `VITE_API_URL`, `VITE_AUTH_URL`. **Deixar pro operador humano** (precisa do domínio `pagar.santos-tech.com` apontando no DNS/Cloudflare). Documente os passos no commit.

- [ ] **Step 3: commit**

```bash
git add infra/Dockerfile.pay-web
git commit -m "feat(pay-web): Dockerfile estático (nginx) para deploy"
```

---

## Parte B — Admin no `org/dashboard` (repo separado: Santos-Techrp/dashboard)

> Trabalhe em `/home/guilherme/projetos/sg/org/dashboard`. É um repositório git **separado** — branch e PR próprios. Siga o padrão local (account-kit para sessão, `web/src/lib` para fetch, componentes `web/src/components/ui` shadcn já existentes). **Antes de codar, leia** `web/src/lib/` e uma página existente em `web/src/pages/admin/` para copiar o padrão de fetch e layout.

### Task B1: client da API de pagamentos no dashboard

- [ ] **Step 1: `web/src/lib/payments.ts`** — funções de fetch para os endpoints admin

```ts
// Ajuste BASE/headers ao padrão do dashboard (account-kit / fetch helper existente).
const BASE = (import.meta.env.VITE_PAYMENTS_URL as string) ?? "https://api.santos-tech.com/payments";

async function j<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, { credentials: "include", ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) } });
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).message ?? `Erro ${res.status}`);
  return res.json();
}

export interface PayProduct { id: number; slug: string; name: string; description: string; priceCents: number; active: boolean; }
export interface PayCharge { id: number; kind: string; amountCents: number; dueDate: string; status: string; createdAt: string; }

export const payments = {
  listProducts: () => j<PayProduct[]>(`/products`),
  createProduct: (p: Omit<PayProduct, "id" | "active">) => j<PayProduct>(`/products`, { method: "POST", body: JSON.stringify(p) }),
  updateProduct: (id: number, p: Partial<PayProduct>) => j<PayProduct>(`/products/${id}`, { method: "PUT", body: JSON.stringify(p) }),
  listCharges: (status = "") => j<PayCharge[]>(`/charges${status ? `?status=${status}` : ""}`),
};
```

- [ ] **Step 2: commit (no repo dashboard)**

```bash
cd /home/guilherme/projetos/sg/org/dashboard
git add web/src/lib/payments.ts
git commit -m "feat(dashboard): client da API de pagamentos"
```

### Task B2: Página de Produtos (admin)

- [ ] **Step 1: `web/src/pages/admin/Produtos.tsx`** — lista + criar/editar (use os componentes shadcn locais: `Card`, `Button`, `Input`, `Label`, `Dialog`, `Badge`)

```tsx
import { useEffect, useState } from "react";
import { payments, type PayProduct } from "../../lib/payments";
import { Button } from "../../components/ui/button";
import { Card } from "../../components/ui/card";
import { Input } from "../../components/ui/input";
import { Label } from "../../components/ui/label";

export default function ProdutosPage() {
  const [list, setList] = useState<PayProduct[]>([]);
  const [form, setForm] = useState({ slug: "", name: "", description: "", priceReais: "" });
  const load = () => payments.listProducts().then(setList).catch(() => {});
  useEffect(() => { load(); }, []);
  async function create() {
    await payments.createProduct({ slug: form.slug, name: form.name, description: form.description,
      priceCents: Math.round(parseFloat(form.priceReais) * 100) });
    setForm({ slug: "", name: "", description: "", priceReais: "" });
    load();
  }
  return (
    <div className="p-6 space-y-6">
      <h1 className="text-2xl font-bold">Produtos</h1>
      <Card className="p-4 grid gap-3 max-w-xl">
        <div><Label>Slug (URL pública)</Label><Input value={form.slug} onChange={(e) => setForm({ ...form, slug: e.target.value })} placeholder="matricula-2026" /></div>
        <div><Label>Nome</Label><Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></div>
        <div><Label>Descrição</Label><Input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} /></div>
        <div><Label>Preço (R$)</Label><Input value={form.priceReais} onChange={(e) => setForm({ ...form, priceReais: e.target.value })} placeholder="539.90" /></div>
        <Button onClick={create}>Criar produto</Button>
      </Card>
      <div className="grid gap-2">
        {list.map((p) => (
          <Card key={p.id} className="p-3 flex justify-between items-center">
            <div><div className="font-medium">{p.name}</div><div className="text-xs text-slate-500">/p/{p.slug} · R$ {(p.priceCents / 100).toFixed(2)} · {p.active ? "ativo" : "inativo"}</div></div>
            <Button variant="outline" onClick={() => payments.updateProduct(p.id, { ...p, active: !p.active }).then(load)}>
              {p.active ? "Desativar" : "Ativar"}
            </Button>
          </Card>
        ))}
      </div>
    </div>
  );
}
```

- [ ] **Step 2: registrar a rota** no router do dashboard (siga onde as outras páginas admin são registradas — ex.: `web/src/App.tsx` ou arquivo de rotas; adicione `/admin/produtos` apontando para `ProdutosPage`, protegido como as demais admin) e adicione o item no menu/sidebar admin.

- [ ] **Step 3: build + commit**

Run: `cd /home/guilherme/projetos/sg/org/dashboard/web && bun run build`
```bash
cd /home/guilherme/projetos/sg/org/dashboard
git add web/src
git commit -m "feat(dashboard): página admin de produtos"
```

### Task B3: Página de Cobranças/Inadimplência (admin)

- [ ] **Step 1: `web/src/pages/admin/Cobrancas.tsx`** — lista com filtro de status

```tsx
import { useEffect, useState } from "react";
import { payments, type PayCharge } from "../../lib/payments";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";

const STATUS = ["", "pending", "paid", "expired"] as const;
const LABEL: Record<string, string> = { "": "Todas", pending: "Pendentes", paid: "Pagas", expired: "Vencidas" };

export default function CobrancasPage() {
  const [status, setStatus] = useState<string>("");
  const [list, setList] = useState<PayCharge[]>([]);
  useEffect(() => { payments.listCharges(status).then(setList).catch(() => {}); }, [status]);
  return (
    <div className="p-6 space-y-4">
      <h1 className="text-2xl font-bold">Cobranças</h1>
      <div className="flex gap-2">
        {STATUS.map((s) => <Button key={s} variant={s === status ? "default" : "outline"} onClick={() => setStatus(s)}>{LABEL[s]}</Button>)}
      </div>
      <div className="grid gap-2">
        {list.map((c) => (
          <Card key={c.id} className="p-3 flex justify-between text-sm">
            <span>#{c.id} · {c.kind} · vence {c.dueDate}</span>
            <span>R$ {(c.amountCents / 100).toFixed(2)} · <b>{c.status}</b></span>
          </Card>
        ))}
      </div>
    </div>
  );
}
```

- [ ] **Step 2: rota `/admin/cobrancas` + item no menu** (mesmo padrão da B2).

- [ ] **Step 3: build + commit**

Run: `cd /home/guilherme/projetos/sg/org/dashboard/web && bun run build`
```bash
cd /home/guilherme/projetos/sg/org/dashboard
git add web/src
git commit -m "feat(dashboard): página admin de cobranças/inadimplência"
```

---

## Self-Review (cobertura do spec)
- ✅ pay-web público, sessão compartilhada + redirect de auth → A2 (`redirectToLogin`).
- ✅ CPF/telefone na tela + checkbox shadcn "salvar" → A3 (`PayerForm`).
- ✅ Carrinho + checkout → A4 (`Cart.tsx`).
- ✅ SSE de status → A4 (`Checkout.tsx`, `EventSource`).
- ✅ QR + copia-e-cola → A3/A4 (`PixView`/`CopyField`).
- ✅ Admin de produtos no dashboard → B2. Cobranças/inadimplência → B3.
- ✅ shadcn reusado do dashboard → A1.
- ⚠️ Deploy do pay-web e DNS de `pagar.santos-tech.com` → A5 (operador humano cria o app na Coolify + aponta o domínio).
- ⚠️ Os subagentes do dashboard devem **ler o padrão local** (auth/fetch/rotas) antes de codar — os snippets são o esqueleto, adaptáveis ao que já existe.
```
