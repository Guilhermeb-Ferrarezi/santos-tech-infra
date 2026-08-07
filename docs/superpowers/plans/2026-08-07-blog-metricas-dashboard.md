# Blog — Métricas: Dashboard visual (dashboard/web) Implementation Plan

> **For agentic workers:** superpowers:subagent-driven-development/executing-plans não
> instalados — execute task por task diretamente. Gate deste repo (`dashboard/CLAUDE.md`):
> `bun run lint` + `bun run build` **e** cobertura E2E (Playwright smoke + screenshot
> conferido visualmente) — obrigatório pra tela nova, não é opcional.

**Goal:** Nova tela `/admin/blog/metricas` em `dashboard/web`: grid de cards
arrastáveis/redimensionáveis mostrando os dados coletados pelo Plano 1
(pageviews, visitantes, CTA, conversão, série temporal, top posts, referrers,
UTM source, dispositivo/país), com Recharts animado, 3 modos de visualização
por card (Lista/Proporção/Tendência) e layout salvo em `localStorage`.

**Architecture:** `lib/blog-metrics.ts` (hooks TanStack Query, um por
endpoint — mesmo padrão de `lib/blog-admin.ts`) → `components/blog-metrics/`
(cards reutilizáveis: `StatCard` com sparkline, `RankingCard` com os 3 modos,
`TimeseriesCard`) → `pages/admin/blog/Metricas.tsx` (monta a grid via
`react-grid-layout`, injeta os cards, persiste posição/tamanho no
`localStorage`). Reaproveita `PageShell`, `PageHeader`, os componentes de
`components/charts/` (Area/Bar/DonutChartCard) e a permissão `blog_posts:read`
já existentes — nenhuma peça de infra nova além da lib de grid.

**Tech Stack:** React 19, TanStack Query, Recharts 3 (já é dependência),
`react-grid-layout` (novo), TypeScript strict.

**Este é o Plano 3 de 3** (spec: `docs/superpowers/specs/2026-08-06-blog-metricas-design.md`).
Depende do Plano 1 (endpoints `GET /blog/metrics/*` em `api.santos-tech.com`,
já em produção — path corrigido de `/admin/blog/metrics/*` pra `/blog/metrics/*`
no commit `547d5f6`, consistente com `/blog/posts`) e do Plano 2 (beacon
gerando dado real, já em produção).

---

## Task 1: Dependência (`react-grid-layout`)

**Files:**
- Modify: `web/package.json`, `web/bun.lock`

- [ ] **Step 1: Instalar**

Run: `cd web && bun add react-grid-layout@^2`

A v2 reestruturou a API (exports separados `.`/`core`/`react`/`legacy`) e
**já vem com tipos TypeScript embutidos** — `@types/react-grid-layout` é só
um stub vazio hoje (confirmado: `README.md` do pacote diz "you don't need
@types/react-grid-layout installed"), não instale. Este plano usa
especificamente o subpath **`react-grid-layout/legacy`**, que reexpõe a API
plana clássica (v1) — mesmos props (`layout`, `onLayoutChange`,
`draggableHandle`, `resizeHandle`, `autoSize`...) que o resto deste plano
usa, com tipos reais (confirmado lendo `node_modules/react-grid-layout/dist/legacy.d.ts`).

- [ ] **Step 2: Confirmar que build não quebrou**

Run: `cd web && bun run build`
Expected: sem erro (só adicionar a dependência não muda nada ainda).

- [ ] **Step 3: Commit**

```bash
git add web/package.json web/bun.lock
git commit -m "chore(web): adiciona react-grid-layout pro dashboard de métricas do blog"
```

---

## Task 2: Cliente de dados (`lib/blog-metrics.ts`)

Mesmo padrão de `lib/blog-admin.ts` (que já usa `authApi` de `@/lib/api`, um
hook `useQuery` por endpoint). Todos os 6 endpoints de agregação recebem
`from`/`to` (YYYY-MM-DD) e `postSlug` opcional.

**Files:**
- Create: `web/src/lib/blog-metrics.ts`

- [ ] **Step 1: Implementar**

```typescript
import { useQuery } from "@tanstack/react-query"
import { authApi } from "@/lib/api"

export interface BlogMetricsOverview {
  pageviews: number
  visitors: number
  ctaClicks: number
  conversionRate: number
  prevPageviews: number
  prevVisitors: number
  prevCtaClicks: number
  prevConversionRate: number
}

export interface BlogMetricsTimeseriesPoint {
  bucket: string
  pageviews: number
}

export interface BlogMetricsTopPost {
  postSlug: string
  views: number
  ctaClicks: number
  conversionRate: number
}

export interface BlogMetricsCount {
  key: string
  count: number
}

export interface BlogMetricsParams {
  from: string
  to: string
  postSlug?: string
}

function qs(params: BlogMetricsParams): string {
  const q = new URLSearchParams({ from: params.from, to: params.to })
  if (params.postSlug) q.set("postSlug", params.postSlug)
  return q.toString()
}

const key = (name: string, params: BlogMetricsParams) => ["blog-metrics", name, params] as const

export function useBlogMetricsOverview(params: BlogMetricsParams) {
  return useQuery({
    queryKey: key("overview", params),
    queryFn: () => authApi<BlogMetricsOverview>(`/blog/metrics/overview?${qs(params)}`),
  })
}

export function useBlogMetricsTimeseries(params: BlogMetricsParams) {
  return useQuery({
    queryKey: key("timeseries", params),
    queryFn: () => authApi<BlogMetricsTimeseriesPoint[]>(`/blog/metrics/timeseries?${qs(params)}`),
  })
}

export function useBlogMetricsTopPosts(params: Pick<BlogMetricsParams, "from" | "to">) {
  return useQuery({
    queryKey: key("top-posts", params),
    queryFn: () => authApi<BlogMetricsTopPost[]>(`/blog/metrics/top-posts?${qs(params)}`),
  })
}

export function useBlogMetricsReferrers(params: BlogMetricsParams) {
  return useQuery({
    queryKey: key("referrers", params),
    queryFn: () => authApi<BlogMetricsCount[]>(`/blog/metrics/referrers?${qs(params)}`),
  })
}

export function useBlogMetricsUTMSource(params: BlogMetricsParams) {
  return useQuery({
    queryKey: key("utm-source", params),
    queryFn: () => authApi<BlogMetricsCount[]>(`/blog/metrics/utm-source?${qs(params)}`),
  })
}

export function useBlogMetricsDevices(params: BlogMetricsParams) {
  return useQuery({
    queryKey: key("devices", params),
    queryFn: () => authApi<BlogMetricsCount[]>(`/blog/metrics/devices?${qs(params)}`),
  })
}

export function useBlogMetricsCountries(params: BlogMetricsParams) {
  return useQuery({
    queryKey: key("countries", params),
    queryFn: () => authApi<BlogMetricsCount[]>(`/blog/metrics/countries?${qs(params)}`),
  })
}

// ── Período ──────────────────────────────────────────────────────────────────

export type PeriodPreset = "7d" | "30d" | "90d"

// toISODate formata em fuso local (não UTC) — evita o "dia errado" perto da
// meia-noite que Date#toISOString() causaria.
function toISODate(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, "0")
  const day = String(d.getDate()).padStart(2, "0")
  return `${y}-${m}-${day}`
}

export function periodToRange(preset: PeriodPreset): { from: string; to: string } {
  const days = preset === "7d" ? 7 : preset === "30d" ? 30 : 90
  const to = new Date()
  const from = new Date()
  from.setDate(from.getDate() - (days - 1))
  return { from: toISODate(from), to: toISODate(to) }
}
```

- [ ] **Step 2: `bun run lint` + `bun run build`**

Run: `cd web && bun run lint && bun run build`
Expected: sem erro.

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/blog-metrics.ts
git commit -m "feat(web): hooks TanStack Query pros endpoints de blog metrics"
```

---

## Task 3: `RankingCard` — lista/proporção/tendência + menu "..."

Card genérico pra qualquer dado no formato `{key, count}[]` (top posts,
referrers, UTM source, dispositivo, país). Menu "..." sempre visível (sutil);
resize handle do `react-grid-layout` só aparece no hover (Task 5 estiliza
isso globalmente, não aqui).

**Files:**
- Create: `web/src/components/blog-metrics/RankingCard.tsx`

- [ ] **Step 1: Implementar**

```tsx
import { useState } from "react"
import { DotsThree, ListBullets, ChartBar as BarIcon, TrendUp } from "@phosphor-icons/react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { BarChartCard } from "@/components/charts/BarChartCard"
import { cn } from "@/lib/utils"

type ViewMode = "list" | "proportion" | "trend"

export function RankingCard({
  title,
  data,
  isLoading,
  color = "var(--brand)",
  className,
}: {
  title: string
  data: { key: string; count: number }[] | undefined
  isLoading: boolean
  color?: string
  className?: string
}) {
  const [mode, setMode] = useState<ViewMode>("list")
  const items = data ?? []
  const max = Math.max(1, ...items.map((d) => d.count))

  return (
    <div className={cn("flex h-full flex-col rounded-xl border border-border bg-card/60 p-4", className)}>
      <div className="mb-3 flex items-center justify-between">
        <p className="text-[11px] uppercase tracking-[0.15em] text-muted-foreground">{title}</p>
        <DropdownMenu>
          <DropdownMenuTrigger className="text-muted-foreground transition-colors hover:text-foreground">
            <DotsThree weight="bold" className="size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => setMode("list")}>
              <ListBullets className="mr-2 size-4" /> Ver como lista
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setMode("proportion")}>
              <BarIcon className="mr-2 size-4" /> Ver como proporção
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setMode("trend")}>
              <TrendUp className="mr-2 size-4" /> Ver como gráfico
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled>Exportar CSV (em breve)</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {isLoading ? (
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">Carregando…</div>
      ) : items.length === 0 ? (
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">Sem dados no período</div>
      ) : mode === "list" ? (
        <div className="flex-1 space-y-0.5 overflow-y-auto">
          {items.map((d) => (
            <div key={d.key} className="flex items-center justify-between border-b border-border/50 py-2 text-sm last:border-0">
              <span className="truncate">{d.key}</span>
              <span className="font-mono text-muted-foreground">{d.count}</span>
            </div>
          ))}
        </div>
      ) : mode === "proportion" ? (
        <div className="flex-1 space-y-3 overflow-y-auto">
          {items.map((d) => (
            <div key={d.key} className="grid grid-cols-[minmax(0,1fr)_2fr_40px] items-center gap-3 text-sm">
              <span className="truncate">{d.key}</span>
              <div className="h-1.5 rounded-full bg-muted">
                <div
                  className="h-full rounded-full"
                  style={{ width: `${(d.count / max) * 100}%`, backgroundColor: color }}
                />
              </div>
              <span className="text-right font-mono text-muted-foreground">{d.count}</span>
            </div>
          ))}
        </div>
      ) : (
        <div className="flex-1">
          <BarChartCard
            title=""
            color={color}
            data={items.map((d) => ({ label: d.key, value: d.count }))}
          />
        </div>
      )}
    </div>
  )
}
```

Nota: `BarChartCard` já renderiza seu próprio título (mesmo componente de
`components/charts/`) — passar `title=""` aqui evita duplicar o rótulo, já que
o `RankingCard` já mostra o título dele mesmo em cima.

- [ ] **Step 2: Confirmar que `DropdownMenu` existe em `components/ui`**

Run: `ls web/src/components/ui/dropdown-menu.tsx`
Expected: arquivo existe (shadcn já tem esse primitivo — padrão do template).
Se não existir: `cd web && bunx shadcn@latest add dropdown-menu`.

- [ ] **Step 3: `bun run lint` + `bun run build`**

Run: `cd web && bun run lint && bun run build`
Expected: sem erro.

- [ ] **Step 4: Commit**

```bash
git add web/src/components/blog-metrics/RankingCard.tsx
git commit -m "feat(web): RankingCard com 3 modos de visualização (lista/proporção/tendência)"
```

---

## Task 4: `StatCard` — card de resumo com sparkline

**Files:**
- Create: `web/src/components/blog-metrics/StatCard.tsx`

- [ ] **Step 1: Implementar**

```tsx
import { ResponsiveContainer, LineChart, Line } from "recharts"
import { type Icon } from "@phosphor-icons/react"
import { cn } from "@/lib/utils"

export function StatCard({
  icon: I,
  label,
  value,
  deltaPct,
  sparkline,
  hint,
  className,
}: {
  icon: Icon
  label: string
  value: string
  /** null = sem comparação (ex.: taxa de conversão não tem "delta" útil). */
  deltaPct: number | null
  sparkline: number[]
  hint?: string
  className?: string
}) {
  const up = deltaPct !== null && deltaPct >= 0
  const sparkColor = deltaPct === null ? "var(--muted-foreground)" : up ? "var(--brand)" : "var(--destructive)"
  const data = sparkline.map((v, i) => ({ i, v }))

  return (
    <div className={cn("rounded-xl border border-border bg-card/60 p-4", className)}>
      <div className="flex items-center justify-between">
        <p className="flex items-center gap-1.5 text-[11px] uppercase tracking-[0.15em] text-muted-foreground">
          <I weight="fill" className="size-3.5" /> {label}
        </p>
      </div>
      <div className="mt-2 flex items-end justify-between gap-3">
        <span className="font-mono text-2xl font-semibold tabular-nums">{value}</span>
        {data.length > 1 && (
          <div className="h-6 w-16 shrink-0">
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={data}>
                <Line type="monotone" dataKey="v" stroke={sparkColor} strokeWidth={2} dot={false} isAnimationActive />
              </LineChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>
      {deltaPct !== null ? (
        <p className={cn("mt-1 text-xs", up ? "text-emerald-500" : "text-destructive")}>
          {up ? "+" : ""}
          {(deltaPct * 100).toFixed(1)}% vs período anterior
        </p>
      ) : hint ? (
        <p className="mt-1 text-xs text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  )
}
```

- [ ] **Step 2: `bun run lint` + `bun run build`**

Run: `cd web && bun run lint && bun run build`
Expected: sem erro.

- [ ] **Step 3: Commit**

```bash
git add web/src/components/blog-metrics/StatCard.tsx
git commit -m "feat(web): StatCard com sparkline animado pros números de resumo"
```

---

## Task 5: Grid customizável + persistência (`GridBoard.tsx`)

Wrapper fino sobre `react-grid-layout`: aceita uma lista de cards com posição
default, salva/lê o arranjo do usuário em `localStorage`. Resize handle só
aparece no hover (CSS), drag só pelo header do card (`draggableHandle`).

**Files:**
- Create: `web/src/components/blog-metrics/GridBoard.tsx`
- Create: `web/src/components/blog-metrics/grid-board.css`

- [ ] **Step 1: CSS do handle (hover-only) e import base do RGL**

`web/src/components/blog-metrics/grid-board.css`:

```css
@import "react-grid-layout/css/styles.css";
@import "react-resizable/css/styles.css";

.grid-board-item {
  transition: box-shadow 0.15s ease;
}
.grid-board-item:hover {
  box-shadow: 0 0 0 1px var(--border);
}
.grid-board-handle {
  position: absolute;
  right: 6px;
  bottom: 6px;
  width: 14px;
  height: 14px;
  cursor: nwse-resize;
  color: var(--muted-foreground);
  opacity: 0;
  transition: opacity 0.15s ease;
}
.grid-board-item:hover .grid-board-handle {
  opacity: 1;
}
.grid-board-drag-handle {
  cursor: grab;
}
.grid-board-drag-handle:active {
  cursor: grabbing;
}
```

- [ ] **Step 2: Implementar o `GridBoard`**

```tsx
import { useCallback, useMemo, useState } from "react"
import GridLayout, { type Layout } from "react-grid-layout/legacy"
import { DotsSixVertical } from "@phosphor-icons/react"
import "./grid-board.css"

export type GridCardDef = {
  id: string
  defaultLayout: { x: number; y: number; w: number; h: number }
  render: () => React.ReactNode
}

function loadLayout(storageKey: string, defs: GridCardDef[]): Layout {
  try {
    const raw = window.localStorage.getItem(storageKey)
    if (!raw) throw new Error("sem layout salvo")
    const saved = JSON.parse(raw) as Layout
    // Só reaproveita o salvo se cobrir exatamente os mesmos cards de hoje —
    // card novo/removido no código invalida o layout salvo e volta ao default
    // (mais simples e mais seguro que tentar fazer merge parcial).
    const savedIds = new Set(saved.map((l) => l.i))
    const defIds = new Set(defs.map((d) => d.id))
    const sameSet = savedIds.size === defIds.size && [...defIds].every((id) => savedIds.has(id))
    if (!sameSet) throw new Error("cards mudaram")
    return saved
  } catch {
    return defs.map((d) => ({ i: d.id, ...d.defaultLayout }))
  }
}

export function GridBoard({ storageKey, cards, cols = 12, rowHeight = 32 }: {
  /** Chave do localStorage — inclua o id do usuário pra não misturar arranjos. */
  storageKey: string
  cards: GridCardDef[]
  cols?: number
  rowHeight?: number
}) {
  const [layout, setLayout] = useState<Layout>(() => loadLayout(storageKey, cards))

  const onLayoutChange = useCallback(
    (next: Layout) => {
      setLayout(next)
      window.localStorage.setItem(storageKey, JSON.stringify(next))
    },
    [storageKey],
  )

  const byId = useMemo(() => new Map(cards.map((c) => [c.id, c])), [cards])

  return (
    <GridLayout
      layout={layout}
      onLayoutChange={onLayoutChange}
      cols={cols}
      rowHeight={rowHeight}
      width={1200}
      autoSize
      draggableHandle=".grid-board-drag-handle"
      resizeHandle={
        <span className="grid-board-handle react-resizable-handle react-resizable-handle-se">
          <DotsSixVertical weight="bold" className="size-3.5 rotate-45" />
        </span>
      }
      margin={[16, 16]}
    >
      {cards.map((c) => (
        <div key={c.id} className="grid-board-item relative overflow-hidden rounded-xl">
          <div className="grid-board-drag-handle absolute inset-x-0 top-0 h-6" aria-hidden />
          {byId.get(c.id)?.render()}
        </div>
      ))}
    </GridLayout>
  )
}
```

Nota: `width={1200}` é fixo porque `GridLayout` (não o `Responsive*`) exige
uma largura numérica — pra MVP isso é aceitável (a tela é usada em desktop,
não precisa responder a breakpoint); registrar como pendência de QoL se
precisar de verdade responsivo depois.

Nota 2: a faixa `grid-board-drag-handle` de 24px no topo de cada card é o que
o usuário agarra pra arrastar — o menu "..." e o conteúdo abaixo continuam
clicáveis normalmente (só a faixa fina do topo inicia o drag).

- [ ] **Step 3: `bun run lint` + `bun run build`**

Run: `cd web && bun run lint && bun run build`
Expected: sem erro (tipos vêm do próprio pacote — ver nota da Task 1; CSS
puro via `@import` não precisa de tipos, `react-resizable` já é dependência
transitiva do `react-grid-layout`, confirmado em `node_modules`).

- [ ] **Step 4: Commit**

```bash
git add web/src/components/blog-metrics/GridBoard.tsx web/src/components/blog-metrics/grid-board.css
git commit -m "feat(web): GridBoard — grid arrastável/redimensionável com layout salvo em localStorage"
```

---

## Task 6: Montar a página (`pages/admin/blog/Metricas.tsx`)

**Files:**
- Create: `web/src/pages/admin/blog/Metricas.tsx`

- [ ] **Step 1: Implementar**

```tsx
import { useMemo, useState } from "react"
import { Eye, Users, HandTap, Target } from "@phosphor-icons/react"
import { useMe } from "@/lib/queries"
import { PageShell } from "@/components/PageShell"
import { AreaChartCard } from "@/components/charts/AreaChartCard"
import { StatCard } from "@/components/blog-metrics/StatCard"
import { RankingCard } from "@/components/blog-metrics/RankingCard"
import { GridBoard, type GridCardDef } from "@/components/blog-metrics/GridBoard"
import {
  useBlogMetricsOverview,
  useBlogMetricsTimeseries,
  useBlogMetricsTopPosts,
  useBlogMetricsReferrers,
  useBlogMetricsUTMSource,
  useBlogMetricsDevices,
  useBlogMetricsCountries,
  periodToRange,
  type PeriodPreset,
} from "@/lib/blog-metrics"
import { cn } from "@/lib/utils"

const PRESETS: { value: PeriodPreset; label: string }[] = [
  { value: "7d", label: "7 dias" },
  { value: "30d", label: "30 dias" },
  { value: "90d", label: "90 dias" },
]

function pct(n: number) {
  return `${(n * 100).toFixed(1)}%`
}

export default function BlogMetricas() {
  const { data: me } = useMe()
  const [period, setPeriod] = useState<PeriodPreset>("7d")
  const range = useMemo(() => periodToRange(period), [period])

  const overview = useBlogMetricsOverview(range)
  const timeseries = useBlogMetricsTimeseries(range)
  const topPosts = useBlogMetricsTopPosts(range)
  const referrers = useBlogMetricsReferrers(range)
  const utmSource = useBlogMetricsUTMSource(range)
  const devices = useBlogMetricsDevices(range)
  const countries = useBlogMetricsCountries(range)

  const o = overview.data
  const delta = (curr: number, prev: number) => (prev > 0 ? (curr - prev) / prev : curr > 0 ? 1 : 0)

  const sparkFrom = (series: { pageviews: number }[] | undefined, pick: (p: { pageviews: number }) => number) =>
    (series ?? []).map(pick)

  const cards: GridCardDef[] = [
    {
      id: "stat-pageviews",
      defaultLayout: { x: 0, y: 0, w: 3, h: 4 },
      render: () => (
        <StatCard
          icon={Eye}
          label="Pageviews"
          value={o ? String(o.pageviews) : "—"}
          deltaPct={o ? delta(o.pageviews, o.prevPageviews) : null}
          sparkline={sparkFrom(timeseries.data, (p) => p.pageviews)}
        />
      ),
    },
    {
      id: "stat-visitors",
      defaultLayout: { x: 3, y: 0, w: 3, h: 4 },
      render: () => (
        <StatCard
          icon={Users}
          label="Visitantes únicos"
          value={o ? String(o.visitors) : "—"}
          deltaPct={o ? delta(o.visitors, o.prevVisitors) : null}
          sparkline={sparkFrom(timeseries.data, (p) => p.pageviews)}
        />
      ),
    },
    {
      id: "stat-cta",
      defaultLayout: { x: 6, y: 0, w: 3, h: 4 },
      render: () => (
        <StatCard
          icon={HandTap}
          label="Cliques no CTA"
          value={o ? String(o.ctaClicks) : "—"}
          deltaPct={o ? delta(o.ctaClicks, o.prevCtaClicks) : null}
          sparkline={sparkFrom(timeseries.data, (p) => p.pageviews)}
        />
      ),
    },
    {
      id: "stat-conversion",
      defaultLayout: { x: 9, y: 0, w: 3, h: 4 },
      render: () => (
        <StatCard
          icon={Target}
          label="Taxa de conversão"
          value={o ? pct(o.conversionRate) : "—"}
          deltaPct={null}
          hint="cliques / visitantes"
          sparkline={sparkFrom(timeseries.data, (p) => p.pageviews)}
        />
      ),
    },
    {
      id: "timeseries",
      defaultLayout: { x: 0, y: 4, w: 12, h: 8 },
      render: () => (
        <AreaChartCard
          title="Pageviews por dia"
          data={(timeseries.data ?? []).map((p) => ({
            date: new Date(p.bucket).toLocaleDateString("pt-BR", { day: "2-digit", month: "2-digit" }),
            pageviews: p.pageviews,
          }))}
          series={[{ key: "pageviews", label: "Pageviews", color: "var(--brand)" }]}
        />
      ),
    },
    {
      id: "top-posts",
      defaultLayout: { x: 0, y: 12, w: 7, h: 8 },
      render: () => (
        <RankingCard
          title="Posts mais vistos"
          isLoading={topPosts.isLoading}
          data={(topPosts.data ?? []).map((p) => ({ key: p.postSlug, count: p.views }))}
        />
      ),
    },
    {
      id: "referrers",
      defaultLayout: { x: 7, y: 12, w: 5, h: 8 },
      render: () => <RankingCard title="Referrers" isLoading={referrers.isLoading} data={referrers.data} />,
    },
    {
      id: "utm-source",
      defaultLayout: { x: 0, y: 20, w: 6, h: 8 },
      render: () => <RankingCard title="Origem (UTM source)" isLoading={utmSource.isLoading} data={utmSource.data} />,
    },
    {
      id: "devices",
      defaultLayout: { x: 6, y: 20, w: 3, h: 8 },
      render: () => <RankingCard title="Dispositivo" isLoading={devices.isLoading} data={devices.data} />,
    },
    {
      id: "countries",
      defaultLayout: { x: 9, y: 20, w: 3, h: 8 },
      render: () => <RankingCard title="País" isLoading={countries.isLoading} data={countries.data} />,
    },
  ]

  return (
    <PageShell scroll contentClassName="p-6">
      {/* Sem <PageHeader title/subtitle> aqui: o componente hoje só injeta
          `actions` via portal (título real vem do label em nav.ts) — chamá-lo
          sem actions não renderiza nada, então é só overhead. */}
      <div className="mb-4 flex rounded-lg border border-border bg-card/50 p-0.5 text-xs">
        {PRESETS.map((p) => (
          <button
            key={p.value}
            onClick={() => setPeriod(p.value)}
            className={cn(
              "rounded-md px-3 py-1.5 font-medium transition-colors",
              period === p.value ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {p.label}
          </button>
        ))}
      </div>
      {overview.isError ? (
        <div className="rounded-xl border border-dashed border-border py-24 text-center text-sm text-muted-foreground">
          Não foi possível carregar as métricas agora.
        </div>
      ) : (
        <GridBoard storageKey={`blog-metricas-layout:${me?.userId ?? "anon"}`} cards={cards} />
      )}
    </PageShell>
  )
}
```

Nota: `sparkline` dos 4 `StatCard` usa a MESMA série (`pageviews` por dia) —
é o único dado com granularidade temporal que os endpoints devolvem no MVP; a
tendência exata de "cliques no CTA por dia" ficaria pra uma iteração futura
que adicionasse uma `timeseries` filtrada por `type=cta_click` (registrar em
`PENDENCIAS.md` se quiser essa granularidade depois — YAGNI por ora).

- [ ] **Step 2: `bun run lint` + `bun run build`**

Run: `cd web && bun run lint && bun run build`
Expected: sem erro (`HandTap` já confirmado existente em `@phosphor-icons/react`;
`Me.userId` já confirmado no tipo do `account-kit` — sem incerteza aqui).

- [ ] **Step 3: Commit**

```bash
git add web/src/pages/admin/blog/Metricas.tsx
git commit -m "feat(web): monta a tela de métricas do blog com grid de cards"
```

---

## Task 7: Rota + navegação

**Files:**
- Modify: `web/src/App.tsx`
- Modify: `web/src/lib/nav.ts`

- [ ] **Step 1: Adicionar a rota**

Em `web/src/App.tsx`, dentro do bloco de rotas Blog **read** (o mesmo grupo de
`/admin/blog` e `/admin/blog/categorias`, linhas ~324-331):

```tsx
    {
      element: <Protected adminOnly permission={{ resource: "blog_posts", action: "read" }}><AppShell /></Protected>,
      children: [
        { path: "/admin/blog", lazy: () => import("@/pages/admin/blog/Posts").then(m => ({ Component: m.default })) },
        { path: "/admin/blog/categorias", lazy: () => import("@/pages/admin/blog/Categorias").then(m => ({ Component: m.default })) },
        { path: "/admin/blog/metricas", lazy: () => import("@/pages/admin/blog/Metricas").then(m => ({ Component: m.default })) },
      ],
    },
```

- [ ] **Step 2: Adicionar ao menu**

Em `web/src/lib/nav.ts`, no grupo `"Blog"` (linhas ~166-190), adicionar um
item entre "Posts" e "Categorias":

```typescript
      {
        to: "/admin/blog/metricas",
        label: "Métricas",
        icon: ChartBar,
        adminOnly: true,
        permission: { resource: "blog_posts", action: "read" },
        keywords: "blog metricas analytics pageviews visitantes conversao cta trafego",
      },
```

(`ChartBar` já está importado no topo do arquivo — usado em "Email → Métricas"
e "Pagamentos → Dashboard".)

- [ ] **Step 3: `bun run lint` + `bun run build`**

Run: `cd web && bun run lint && bun run build`
Expected: sem erro.

- [ ] **Step 4: Commit**

```bash
git add web/src/App.tsx web/src/lib/nav.ts
git commit -m "feat(web): registra rota e item de menu de Métricas do blog"
```

---

## Task 8: Cobertura E2E (obrigatória neste repo)

**Files:**
- Modify: `web/e2e/smoke.spec.ts`
- Modify: `web/e2e/fixtures.ts`

- [ ] **Step 1: Estender o mock de `/blog/**` em `fixtures.ts`**

Em `web/e2e/fixtures.ts`, dentro do handler `p.route("https://api.santos-tech.com/blog/**", ...)`
(linhas ~229-235), adicionar os casos de `/blog/metrics/*` ANTES do
`return json(r, UNIVERSAL)` final:

```typescript
  await p.route("https://api.santos-tech.com/blog/**", (r) => {
    const u = path(r)
    if (/\/blog\/categories/.test(u)) return json(r, [BLOG_CAT])
    if (/\/blog\/posts\/[^/]+$/.test(u)) return json(r, BLOG_POST)
    if (/\/blog\/posts/.test(u)) return json(r, { items: [BLOG_POST], page: 1, pageSize: 20, total: 1 })
    if (/\/blog\/metrics\/overview/.test(u)) {
      return json(r, {
        pageviews: 120, visitors: 80, ctaClicks: 6, conversionRate: 0.075,
        prevPageviews: 100, prevVisitors: 70, prevCtaClicks: 5, prevConversionRate: 0.071,
      })
    }
    if (/\/blog\/metrics\/timeseries/.test(u)) {
      return json(r, [
        { bucket: "2026-08-01T00:00:00Z", pageviews: 12 },
        { bucket: "2026-08-02T00:00:00Z", pageviews: 18 },
        { bucket: "2026-08-03T00:00:00Z", pageviews: 15 },
      ])
    }
    if (/\/blog\/metrics\/top-posts/.test(u)) {
      return json(r, [{ postSlug: BLOG_POST.slug, views: 40, ctaClicks: 3, conversionRate: 0.075 }])
    }
    if (/\/blog\/metrics\/(referrers|utm-source|devices|countries)/.test(u)) {
      return json(r, [{ key: "google.com", count: 20 }, { key: "direto", count: 10 }])
    }
    return json(r, UNIVERSAL)
  })
```

- [ ] **Step 2: Adicionar a rota ao smoke**

Em `web/e2e/smoke.spec.ts`, na lista `routes` (linhas ~118-121, seção Blog),
adicionar:

```typescript
  ["blog-metricas", "/admin/blog/metricas"],
```

- [ ] **Step 3: Rodar o smoke**

Run: `cd web && bun run e2e`
Expected: PASS pra `blog-metricas` (desktop + mobile), screenshots gerados em
`e2e/screenshots/smoke-blog-metricas.png` e `mobile-blog-metricas.png`.

- [ ] **Step 4: Conferir os screenshots visualmente**

Abrir os dois PNGs gerados e checar: casco `PageShell` correto, cards
visíveis com os dados mockados, sem sobreposição, sem elemento cortado. Se o
`width={1200}` fixo do `GridBoard` (Task 5) estourar no screenshot mobile
(390px), é esperado ficar com scroll horizontal — **registrar em
`PENDENCIAS.md`** como item de QoL (grid responsivo é trabalho de iteração
futura, não do MVP) em vez de tentar resolver agora.

- [ ] **Step 5: Commit**

```bash
git add web/e2e/smoke.spec.ts web/e2e/fixtures.ts
git commit -m "test(e2e): cobertura de smoke pra tela de Métricas do blog"
```

---

## Task 9: Verificação final e deploy

- [ ] **Step 1: Gate completo**

Run:
```bash
cd web
bun run lint
bun run build
bun run e2e
```
Expected: os três verdes.

- [ ] **Step 2: Push**

```bash
git push
```

- [ ] **Step 3: Confirmar o deploy**

Auto-deploy via Coolify (mesmo fluxo do resto da sessão) — checar a tag da
imagem do container do `dashboard-web` bater com o commit do Step 2.

- [ ] **Step 4: Verificação manual no navegador de produção**

Abrir `santos-tech.com/dashboard/admin/blog/metricas` logado como admin,
confirmar: os cards carregam dado real (não mockado), o período 7/30/90 dias
recalcula os números, arrastar um card move os outros, redimensionar
funciona, recarregar a página mantém o arranjo customizado (persistido em
`localStorage`), o menu "..." troca lista/proporção/gráfico em pelo menos um
`RankingCard`. Screenshot final da tela funcionando.

---

## Fora de escopo (registrar em `PENDENCIAS.md`, não implementar agora)

- Grid responsivo (`ResponsiveGridLayout` com breakpoints) — o MVP usa largura
  fixa de 1200px.
- Sparkline de CTA/conversão com granularidade própria (hoje reaproveita a
  série de pageviews).
- Exportação CSV (botão já existe no menu, `disabled`).
- Funil de leitura, Web Vitals, feed ao vivo — já eram fora de escopo desde a
  spec original.
