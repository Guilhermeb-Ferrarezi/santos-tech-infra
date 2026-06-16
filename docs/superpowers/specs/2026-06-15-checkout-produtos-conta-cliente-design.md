# Checkout de Produtos com Conta de Cliente (Fase 2) — Design

**Data:** 2026-06-15 · **Projetos:** `apps/payments-go` (backend) + `apps/pay-web` (novo, checkout do cliente) + `org/dashboard` (admin)

## Objetivo

Permitir que a Santos Tech **venda produtos via link público**: cada produto tem uma URL
fixa (`pagar.santos-tech.com/p/{slug}`) que é enviada ao cliente. O cliente abre, **se
autentica** (login único Santos Tech), o produto entra no **carrinho** (a "conta"), ele
informa CPF/telefone na própria tela, **paga via Pix** (Dotfy) e vê a confirmação **em
tempo real (SSE)**. Toda cobrança fica ligada a um cliente autenticado, com **histórico**.

Estende a Fase 1 (Pix/Dotfy, já em produção). Cartão (Stripe) é outra fase, separada.

## Decisões (alinhadas)

- **Auth do cliente:** login único Santos Tech (`api-go`: Google ou e-mail+senha). Como
  `pagar.santos-tech.com` é subdomínio, o cookie `access_token` (Domain `.santos-tech.com`)
  já é compartilhado — o `payments-go` valida com o `verifyToken` existente. **Toda
  transação exige sessão.**
- **CPF/telefone:** coletados **na própria tela de pagamento**, com **checkbox shadcn**
  "salvar para próximas transações". Marcado → persiste em `pay_customers`; desmarcado →
  usa só na cobrança atual (vai pro Dotfy como `payerTaxId`, não persiste).
- **Conta = carrinho + histórico.** Carrinho fica no **Redis** (efêmero, TTL); histórico
  (cobranças pagas + itens) no Postgres.
- **Produtos:** catálogo próprio (`pay_products`), CRUD no **dashboard** (`org/dashboard`).
- **Status ao vivo:** **SSE** (não polling), via **Redis pub/sub** (webhook publica → streams empurram).
- **Admin:** gerenciado no **`org/dashboard`** (web React + shadcn já existentes), consumindo
  os endpoints admin do `payments-go`. Sem tela admin no pay-web.

## Arquitetura

### Redis (efêmeros — evita tabelas de carrinho/estado no Postgres)
- **Carrinho:** `cart:{userId}` → JSON `[{productId, quantity}]`, TTL 7 dias.
- **SSE pub/sub:** canal `pay:charge:{token}`. O webhook `CHARGE_PAID` publica `paid`;
  cada handler SSE inscrito no canal empurra o evento ao browser. Funciona com múltiplas
  instâncias (não depende de estado in-process).
- `payments-go` ganha `REDIS_URL` (go-redis/v9, mesmo Redis do ecossistema).

### Postgres (persistente — novas tabelas `pay_*`)
- `pay_products` — `id`, `slug` (único), `name`, `description`, `price_cents`, `active`, `created_at`.
- `pay_customers` — `id`, `user_id` (único, do auth central), `tax_id`, `phone`, `name`, `email`, `created_at`.
  Criado ao 1º acesso autenticado; `tax_id`/`phone` preenchidos só se o cliente optar por salvar.
- `pay_charges` (existente) — **+** `customer_id`, `public_token` (aleatório p/ a URL), `payer_tax_id` (snapshot).
- `pay_charge_items` — `id`, `charge_id`, `product_id`, `name`, `price_cents`, `quantity` (snapshot do que foi pago).
- (Carrinho NÃO é tabela — fica no Redis.)

### Fluxo de checkout
1. Cliente abre `pagar.santos-tech.com/p/{slug}` → `GET /products/{slug}` (público) mostra o produto.
2. "Adicionar / Pagar" → se não autenticado, redireciona pro auth central e volta logado.
3. `POST /me/cart {slug}` → adiciona no carrinho (Redis). Pode repetir com outros produtos.
4. Checkout: a tela coleta CPF/telefone (se faltam) + checkbox "salvar". `POST /me/cart/checkout
   {taxId, phone, save}` → soma o carrinho, cria a cobrança no Dotfy (`value` em reais),
   grava `pay_charges` + `pay_charge_items`, limpa o carrinho, persiste `pay_customers` se `save=true`.
   Retorna `{ token, brCode, qrCode, amount }`.
5. Tela mostra QR + copia-e-cola e abre `EventSource` em `GET /pay/{token}/events` (SSE).
6. Webhook `CHARGE_PAID` → marca paga → publica em `pay:charge:{token}` → SSE empurra `paid`
   → tela vira "Pagamento confirmado! ✅".
7. Histórico: `GET /me/charges` (cobranças pagas do cliente + itens).

### Endpoints (`payments-go`)
**Admin** (`requireAdmin`, consumidos pelo dashboard):
- `POST/GET /products` · `GET/PUT/DELETE /products/{id}` — CRUD catálogo.
- (Fase 1 já tem `/students`, `/plans`, `/subscriptions`, `/charges`, `/students/{id}/charges`.)

**Cliente** (`authGuard` — logado; consumidos pelo pay-web):
- `GET /products/{slug}` — **público** (mostrar antes de logar).
- `GET /me/customer` · `PUT /me/customer {taxId, phone}` — perfil de pagamento.
- `GET /me/cart` · `POST /me/cart {slug}` · `DELETE /me/cart/{productId}` — carrinho (Redis).
- `POST /me/cart/checkout {taxId, phone, save}` → cria a cobrança Pix do carrinho.
- `GET /me/charges` — histórico do cliente.
- `GET /pay/{token}` — dados da cobrança pra tela (DTO público mínimo).
- `GET /pay/{token}/events` — **SSE** do status.

### Frontend `apps/pay-web` (React 19 + Vite + Tailwind + shadcn/ui)
Checkout público do cliente. Rotas: `/p/:slug` (produto), `/cart`, `/pay/:token` (QR +
`EventSource`), `/sucesso`, `/historico`. `lib/api.ts` com `credentials:'include'`; em 401
redireciona pro auth central (`auth.santos-tech.com/...?redirect=`) e volta. Componentes
pequenos: `ProductCard`, `CartView`, `PixCheckout` (QR + copia-e-cola + **campos CPF/telefone
+ checkbox shadcn "salvar"**), `StatusBadge`, `SuccessScreen`, `ErrorScreen`. Paleta Santos
Tech (azul `#187ABF` / teal `#0DB88F`). Deploy estático na Coolify (igual `auth-web`),
domínio `pagar.santos-tech.com`.

### Admin no `org/dashboard` (web React + shadcn já existentes)
Páginas novas em `dashboard-web/src/pages/admin/`:
- **Produtos:** lista + criar/editar/desativar (consome `/products` admin do payments-go).
- **Cobranças/inadimplência:** lista de `charges` com filtro por status (consome `/charges`).
- Segue o padrão do dashboard (account-kit p/ sessão, fetch ao `api.santos-tech.com/payments`,
  componentes shadcn). Sem Postgres próprio — tudo via a API de pagamentos.

### Segurança
- `/me/*` e checkout exigem sessão válida (`authGuard`). Cobrança sempre ligada ao `customer`.
- `public_token` aleatório (não-enumerável) na URL da tela; DTO público expõe só o necessário
  (valor, brCode, qrCode, status, 1º nome, vencimento) — sem CPF/email.
- CORS com credenciais liberando `pagar.santos-tech.com`.
- Webhook mantém HMAC fail-closed (Fase 1).
- SSE: stream só lê status por token; sem dados sensíveis.

## Não-objetivos (YAGNI)
Cartão (Stripe — fase à parte), cupons/descontos, quantidade editável (qty=1 no MVP, schema
suporta), múltiplas moedas, reembolso pela tela, notificações além do email.

## Decomposição — 2 planos
1. **Backend (`payments-go`):** Redis (carrinho + pub/sub), `pay_products`/`pay_customers`/
   `pay_charge_items` + colunas em `pay_charges`, endpoints admin (produtos) e cliente
   (`/me/*`, `/pay/{token}`, SSE), checkout. Testável via API.
2. **Frontends:** `apps/pay-web` (checkout do cliente) **+** `org/dashboard` (admin de
   produtos e cobranças). Consomem a API do plano 1.
