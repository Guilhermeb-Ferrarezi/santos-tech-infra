# Blog — Métricas: Beacon no blog/web Implementation Plan

> **For agentic workers:** superpowers:subagent-driven-development/executing-plans não estão
> instalados neste ambiente — execute task por task diretamente (Edit/Bash), com o mesmo
> rigor: cada task termina com `bun run lint` + `bun run build` passando, e verificação
> manual (não há suíte de teste JS neste repo — ver `blog/CLAUDE.md`, só lint+build).

**Goal:** O blog público (`blog/web`) passa a mandar um beacon anônimo pro
`POST /public/blog/events` (já em produção, ver Plano 1) a cada pageview e a cada
clique no botão de CTA ("Agendar aula experimental") de dentro do conteúdo do post.

**Architecture:** Um módulo cliente novo (`lib/blog-analytics.ts`) gera/persiste
`visitorId`/`sessionId` anônimos e expõe `trackPageview`/`trackCtaClick` (fetch
fire-and-forget, nunca bloqueia UI, nunca lança erro pro chamador). Pageview é
disparado de `PublicLayout` (já usa `useLocation`, envolve todas as rotas). CTA
click é detectado dentro do `handleContentClick` que `PostDetail.tsx` já usa pra
interceptar links do conteúdo (`dangerouslySetInnerHTML`) — sem alterar o
template/HTML dos posts.

**Tech Stack:** React 19, React Router 7, TypeScript strict, `fetch` nativo (mesmo
padrão de `lib/blog-api.ts`, sem lib nova).

**Este é o Plano 2 de 3.** Depende do Plano 1 (backend) já estar em produção
(está — `POST /public/blog/events` responde 204, verificado em
`docs/superpowers/plans/2026-08-06-blog-metricas-backend.md`). O Plano 3
(dashboard visual) consome os dados que este plano começa a gerar.

---

## Task 1: Cliente de analytics (`lib/blog-analytics.ts`)

**Files:**
- Create: `web/src/lib/blog-analytics.ts`

- [ ] **Step 1: Implementar**

```typescript
// Beacon anônimo do blog analytics — dispara pageview/cta_click pro backend
// (ver santos-tech-infra/apps/api-go/{blog_analytics.go,handlers_blog_analytics.go}
// e docs/openapi.yaml, tag "Blog Analytics"). Fire-and-forget: nunca bloqueia a UI
// e nunca lança erro pro chamador — analytics não pode quebrar a leitura do blog.

const BASE_URL = import.meta.env.VITE_BLOG_API_URL ?? "https://api.santos-tech.com"

const VISITOR_ID_KEY = "blog_visitor_id"
const SESSION_ID_KEY = "blog_session_id"

// crypto.randomUUID() existe em todo navegador com suporte a ES2020+ (mesmo
// baseline do resto do app — sem polyfill).
function randomId(): string {
  return crypto.randomUUID()
}

// getOrCreateId lê o id persistido no storage informado, ou gera e grava um novo
// na primeira vez. localStorage → visitorId (~permanente, define "visitante
// único"); sessionStorage → sessionId (esvazia quando a aba fecha).
function getOrCreateId(storage: Storage, key: string): string {
  const existing = storage.getItem(key)
  if (existing) return existing
  const id = randomId()
  storage.setItem(key, id)
  return id
}

function visitorId(): string {
  return getOrCreateId(window.localStorage, VISITOR_ID_KEY)
}

function sessionId(): string {
  return getOrCreateId(window.sessionStorage, SESSION_ID_KEY)
}

type BlogEventPayload = {
  type: "pageview" | "cta_click"
  path: string
  postSlug?: string
  sessionId: string
  visitorId: string
  referrer?: string
  utmSource?: string
}

// sendBeacon nunca lança: falha de rede/analytics não pode derrubar a leitura
// do post pro usuário. `keepalive: true` deixa o request sobreviver mesmo se a
// aba for fechada logo em seguida (comum em cta_click, que geralmente antecede
// abrir outra aba/navegar embora).
function sendBeacon(payload: BlogEventPayload): void {
  fetch(new URL("/public/blog/events", BASE_URL), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    keepalive: true,
    body: JSON.stringify(payload),
  }).catch(() => {
    // Silencioso de propósito — ver comentário do módulo.
  })
}

function utmSourceFromSearch(search: string): string | undefined {
  const utm = new URLSearchParams(search).get("utm_source")
  return utm && utm.trim() !== "" ? utm : undefined
}

export function trackPageview(path: string, postSlug?: string): void {
  sendBeacon({
    type: "pageview",
    path,
    postSlug,
    sessionId: sessionId(),
    visitorId: visitorId(),
    referrer: document.referrer || undefined,
    utmSource: utmSourceFromSearch(window.location.search),
  })
}

export function trackCtaClick(path: string, postSlug: string): void {
  sendBeacon({
    type: "cta_click",
    path,
    postSlug,
    sessionId: sessionId(),
    visitorId: visitorId(),
    referrer: document.referrer || undefined,
    utmSource: utmSourceFromSearch(window.location.search),
  })
}
```

- [ ] **Step 2: `bun run lint` + type-check**

Run: `cd web && bun run lint && bun run build`
Expected: sem erro (o `build` faz `tsc` — pega qualquer erro de tipo aqui).

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/blog-analytics.ts
git commit -m "feat(blog): cliente de analytics (visitorId/sessionId + beacon)"
```

---

## Task 2: Disparar pageview em toda troca de rota (`PublicLayout.tsx`)

**Files:**
- Modify: `web/src/components/PublicLayout.tsx`

- [ ] **Step 1: Adicionar o `useEffect` de pageview**

Estado atual de `web/src/components/PublicLayout.tsx`:

```tsx
import { Outlet, useLocation } from "react-router-dom"
import { BackToTop } from "@/components/BackToTop"
import { Reveal } from "@/components/Reveal"
import { SiteHeader } from "@/components/SiteHeader"
import { SiteFooter } from "@/components/SiteFooter"

// Casco do blog público: header + conteúdo + rodapé. Sem sidebar, sem ⌘K, sem
// login — é o oposto do shell de admin (ver identidade institucional, §8).
export function PublicLayout() {
  const location = useLocation()
  return (
```

Trocar para:

```tsx
import { useEffect } from "react"
import { Outlet, useLocation } from "react-router-dom"
import { BackToTop } from "@/components/BackToTop"
import { Reveal } from "@/components/Reveal"
import { SiteHeader } from "@/components/SiteHeader"
import { SiteFooter } from "@/components/SiteFooter"
import { trackPageview } from "@/lib/blog-analytics"

// Reconhece /post/:slug (já sem o basename "/blog" — useLocation() devolve o
// path relativo ao <BrowserRouter basename>, ver App.tsx) pra extrair o slug e
// mandar junto no beacon; qualquer outra rota manda postSlug undefined.
const POST_PATH_RE = /^\/post\/([^/]+)\/?$/

// Casco do blog público: header + conteúdo + rodapé. Sem sidebar, sem ⌘K, sem
// login — é o oposto do shell de admin (ver identidade institucional, §8).
export function PublicLayout() {
  const location = useLocation()

  // useEffect (não roda durante o SSR do prerender.mjs — renderToString não
  // executa effects) dispara um pageview a cada troca de pathname. Roda de novo
  // em toda navegação client-side (SPA), não só no load inicial.
  useEffect(() => {
    const match = POST_PATH_RE.exec(location.pathname)
    trackPageview(location.pathname, match?.[1])
    // eslint-disable-next-line react-hooks/exhaustive-deps -- só o pathname deve disparar; search/hash não contam como "nova página" pra analytics
  }, [location.pathname])

  return (
```

- [ ] **Step 2: `bun run lint` + `bun run build`**

Run: `cd web && bun run lint && bun run build`
Expected: sem erro. Se o eslint reclamar do `eslint-disable-next-line` (regra
não configurada nesse projeto), remova o comentário e troque a dependência do
efeito por `[location.pathname]` mesmo assim — o comportamento é o que importa,
o comentário é só documentação.

- [ ] **Step 3: Commit**

```bash
git add web/src/components/PublicLayout.tsx
git commit -m "feat(blog): dispara pageview de analytics em toda troca de rota"
```

---

## Task 3: Disparar cta_click no botão de CTA do post (`PostDetail.tsx`)

O botão "Agendar aula experimental" não é um componente React — é HTML puro
dentro de `content_html` (vem do banco, renderizado via
`dangerouslySetInnerHTML`), sempre um `<a target="_blank">` apontando pra
`santos-tech.com/contato` (ver o bloco de CTA verde no post "Tempo de tela x
tempo de criação"). `handleContentClick` já intercepta cliques dentro desse
container pra tratar links internos — vamos adicionar a detecção do CTA ali,
sem interferir na navegação (o link continua abrindo em nova aba normalmente).

**Files:**
- Modify: `web/src/pages/PostDetail.tsx`

- [ ] **Step 1: Editar `handleContentClick`**

Estado atual (linhas 92-115 de `web/src/pages/PostDetail.tsx`):

```tsx
function handleContentClick(navigate: ReturnType<typeof useNavigate>) {
  return (e: React.MouseEvent<HTMLDivElement>) => {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    const anchor = (e.target as HTMLElement).closest("a")
    if (!anchor || anchor.target === "_blank") return
    const href = anchor.getAttribute("href")
    if (!href) return

    let url: URL
    try {
      url = new URL(href, window.location.href)
    } catch {
      return
    }
    if (url.origin !== window.location.origin) return

    const base = import.meta.env.BASE_URL.replace(/\/$/, "")
    if (base && !url.pathname.startsWith(base)) return

    e.preventDefault()
    const path = (base ? url.pathname.slice(base.length) : url.pathname) || "/"
    navigate(path + url.search + url.hash)
  }
}
```

Trocar para (adiciona a detecção do CTA ANTES do `if (!anchor || anchor.target === "_blank") return`,
já que o link do CTA tem `target="_blank"` e sairia por ali sem disparar nada):

```tsx
function handleContentClick(navigate: ReturnType<typeof useNavigate>, postSlug: string) {
  return (e: React.MouseEvent<HTMLDivElement>) => {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    const anchor = (e.target as HTMLElement).closest("a")
    if (!anchor) return

    const href = anchor.getAttribute("href")
    if (href) {
      let ctaUrl: URL | null = null
      try {
        ctaUrl = new URL(href, window.location.href)
      } catch {
        ctaUrl = null
      }
      // CTA do post ("Agendar aula experimental") sempre aponta pra
      // santos-tech.com/contato — é o evento de conversão do blog (ver spec de
      // métricas). Não bloqueia a navegação, só registra em paralelo.
      if (ctaUrl && ctaUrl.hostname === "santos-tech.com" && ctaUrl.pathname.startsWith("/contato")) {
        trackCtaClick(window.location.pathname, postSlug)
      }
    }

    if (anchor.target === "_blank") return
    if (!href) return

    let url: URL
    try {
      url = new URL(href, window.location.href)
    } catch {
      return
    }
    if (url.origin !== window.location.origin) return

    const base = import.meta.env.BASE_URL.replace(/\/$/, "")
    if (base && !url.pathname.startsWith(base)) return

    e.preventDefault()
    const path = (base ? url.pathname.slice(base.length) : url.pathname) || "/"
    navigate(path + url.search + url.hash)
  }
}
```

- [ ] **Step 2: Importar `trackCtaClick` e passar `postSlug` no call site**

No topo do arquivo, adicionar ao import existente de `@/lib/blog-analytics`
(criar se ainda não existir a linha — vem logo depois do import de
`use-json-ld`):

```tsx
import { trackCtaClick } from "@/lib/blog-analytics"
```

E no JSX (linha ~307), trocar:

```tsx
                onClick={handleContentClick(navigate)}
```

por:

```tsx
                onClick={handleContentClick(navigate, post.slug)}
```

- [ ] **Step 3: `bun run lint` + `bun run build`**

Run: `cd web && bun run lint && bun run build`
Expected: sem erro.

- [ ] **Step 4: Commit**

```bash
git add web/src/pages/PostDetail.tsx
git commit -m "feat(blog): dispara cta_click quando o leitor clica em Agendar aula experimental"
```

---

## Task 4: Verificação manual no navegador (contra o backend real em produção)

Sem Docker/servidor local necessário pra este teste — o cliente já aponta pro
`api.santos-tech.com` de produção (mesma `BASE_URL` usada em dev). CORS já
libera o dev server local (ver Plano 1 — o beacon já responde 204 de
`localhost` nos testes anteriores desta sessão? Confirmar no Step 2; se CORS
bloquear localhost, testar direto contra o build servido em `/blog` — ver
Step 3).

- [ ] **Step 1: Subir o dev server**

Usar o `preview_start` com o launch.json já configurado nesta sessão (`name:
"blog-web"`), ou manualmente: `cd web && bun run dev -- --port 5176 --strictPort`.

- [ ] **Step 2: Abrir um post e checar o Network**

Navegar pra `/post/tempo-de-tela-x-tempo-de-criacao` (local) e checar, via
`read_network_requests`/console do navegador, se saiu um `POST
.../public/blog/events` com `type: "pageview"` e `status: 204`. Se der erro de
CORS (origem `localhost:5176` não permitida no backend), pular pro Step 3 e
testar contra o build de produção (`santos-tech.com/blog`) em vez do dev local.

- [ ] **Step 3: Clicar no botão "Agendar aula experimental" e checar o Network**

Confirmar `POST .../public/blog/events` com `type: "cta_click"` e `postSlug:
"tempo-de-tela-x-tempo-de-criacao"`, `status: 204`, e que o link efetivamente
abre `santos-tech.com/contato` numa nova aba (o clique não pode quebrar a
navegação normal).

- [ ] **Step 4: Confirmar no banco (produção) que os eventos foram gravados**

Consultar `blog_events` via o mesmo acesso SSH+psql já usado no Plano 1,
filtrando pelo `session_id` gerado no teste (visível no payload do Network),
e apagar a(s) linha(s) de teste depois de confirmar.

- [ ] **Step 5: Deploy**

```bash
git push
```

Auto-deploy via Coolify (mesmo fluxo do resto da sessão) — aguardar e conferir
a tag da imagem do container `bdgyyadpz0reyjskzcb4y490-*` bater com o commit
do Step 4 da Task 3, igual feito nos deploys anteriores desta sessão.
