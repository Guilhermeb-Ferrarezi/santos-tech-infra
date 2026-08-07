# Blog — Métricas (analytics first-party)

**Data:** 2026-08-06 · **Projetos:** `apps/api-go` (coleta + agregação), `dashboard/web` (visualização), `blog/web` (beacon)

## Problema

O blog (`santos-tech.com/blog`) não tem nenhuma métrica própria — não dá pra saber
quantas pessoas leem cada post, de onde vêm, nem se o CTA ("Agendar aula
experimental") converte alguma coisa. O loja-3d já resolveu exatamente esse
problema pro marketplace (`services/api-go/internal/analytics`, rota
`/admin/metricas` em 4 abas) — este spec replica o mesmo padrão pro blog, com
escopo de MVP e uma camada de customização visual que o loja-3d não tem.

## Objetivo (MVP)

- Coletar, por post: pageviews, visitantes únicos, referrer, UTM source,
  dispositivo/país, e cliques no botão de CTA ("Agendar aula experimental").
- Uma tela nova em `dashboard/web` mostrando essas métricas, com grid de cards
  que o admin pode reorganizar/redimensionar e cada card com filtro de período
  e modo de visualização (lista / barra de proporção / tendência) próprios.
- Fora do MVP (fica para iteração futura, mesmo padrão do loja-3d que evoluiu em
  9 ciclos): funil de leitura, Web Vitals, feed ao vivo (SSE), exportação CSV
  funcional (o botão existe no menu, mas por ora é `disabled` com tooltip
  "em breve"), scroll depth / tempo de leitura real.

## Decisões (alinhadas com o usuário)

1. **Escopo:** MVP primeiro, não réplica completa do loja-3d de uma vez.
2. **Conversão:** clique no CTA do post (`cta_click`) conta como conversão —
   não existe compra no blog, então este é o evento de maior intenção.
3. **UTM:** `utm_source` faz parte do MVP (dimensão de breakdown).
4. **Onde os dados moram:** `apps/api-go` (mesmo serviço/banco do `blog_posts`) —
   não em `dashboard/api`. Reduz superfície: o blog já confia nesse domínio (CORS
   já liberado pra leitura pública), e mantém conteúdo+métricas no mesmo banco.
5. **Filtros:** cada card tem filtro de período **próprio**, independente — não
   um filtro global único pra tela inteira.
6. **Customização de layout:** drag (reordenar) + resize (redimensionar) entram
   no MVP, não ficam pra depois — decisão consciente do usuário sabendo que isso
   é o subsistema mais caro do pacote inteiro.
7. **Persistência do layout:** `localStorage` do navegador — sem tabela nova,
   sem endpoint novo. Não sincroniza entre máquinas; aceito como trade-off.
8. **Modos de visualização por card:** Lista, Proporção (barra horizontal
   relativa ao maior valor) e Tendência (linha) — padrão inspirado no dashboard
   da Cloudflare (menu "..." por card, resize só aparece no hover).
9. **Gráficos:** Recharts, com animação de entrada.
10. **Permissão de visualização:** reaproveita `blog_posts:read` (quem já edita
    post já vê as métricas) — sem permissão nova no MVP.

## Arquitetura — coleta e agregação (`apps/api-go`)

### Tabela nova `blog_events`

| coluna | tipo | nota |
|--------|------|------|
| id | bigserial PK | |
| type | text | `pageview` \| `cta_click` |
| post_slug | text NULL | nulo fora de página de post (home, categoria) |
| path | text | path completo da página |
| session_id | text | anônimo, gerado no cliente (sessionStorage), dedup de sessão |
| visitor_id | text | anônimo, gerado no cliente (localStorage,~1 ano), dedup de "visitante único" |
| referrer | text NULL | `document.referrer`, truncado a domínio |
| utm_source | text NULL | de `?utm_source=` na URL de entrada |
| device | text | `mobile` \| `desktop` \| `tablet`, parseado do UA no servidor |
| browser | text NULL | parseado do UA no servidor |
| os | text NULL | parseado do UA no servidor |
| country | text NULL | header `CF-IPCountry` |
| created_at | timestamptz | default `now()` |

Índices: `(post_slug, created_at)`, `(type, created_at)`, `(created_at)` (pro job
de retenção). Migração via `apps/api-go/db/schema.sql` + `db/query/blog_events.sql`
(sqlc, seguindo o padrão obrigatório do repo — nada de SQL solto em handler).

`device`/`browser`/`os`/`country` **nunca** vêm do payload do cliente — sempre
derivados no servidor a partir de `User-Agent` e headers (mesma lógica
anti-forja do loja-3d: o cliente não pode inflar métrica mandando dado falso).

### Ingestão

`POST /public/blog/events` — público, sem auth, rate limit `api-go:rl:blogevents:<ip>`
300/min (mesmo número do loja-3d). Payload:

```json
{ "type": "pageview", "path": "/blog/post/tempo-de-tela...", "postSlug": "tempo-de-tela...",
  "sessionId": "...", "visitorId": "...", "referrer": "https://...", "utmSource": "instagram" }
```

`blog/web` chama esse endpoint: uma vez por pageview (`useEffect` na troca de
rota) e uma vez por clique no botão de CTA do post (`type: cta_click`, mesmo
`postSlug`). Como o beacon vai para `api.santos-tech.com`, mesmo domínio que já
serve `GET /public/blog/posts`, **não precisa de CORS novo**.

### Agregação — endpoints admin

Todos sob `GET /admin/blog/metrics/*`, guardados por `authGuard` +
`blog_posts:read`, cada um recebendo `from`/`to` (ISO date) e `postSlug`
(opcional) como query params — não um filtro global de sessão, já que cada card
do front manda seu próprio período:

| Endpoint | Retorna |
|----------|---------|
| `.../overview` | pageviews, visitantes únicos, cliques CTA, taxa de conversão + comparação vs período anterior de mesma duração |
| `.../timeseries` | série diária (ou por hora se `to - from ≤ 24h`) de pageviews, com `generate_series` pra não pular dia sem dado |
| `.../top-posts` | por post: views, cliques CTA, conversão |
| `.../referrers` | top domínios de referrer |
| `.../utm-source` | top `utm_source` |
| `.../devices` | breakdown por `device` e por `country` |

Cada query é um arquivo sqlc nomeado (`db/query/blog_metrics.sql`,
`-- name: BlogMetricsTopReferrers :many` etc.) — sem query builder dinâmico,
mantém o padrão obrigatório do repo.

### Retenção

Job em `apps/worker-go` (mesmo mecanismo do `analytics_purge.go` do loja-3d):
apaga `blog_events` com mais de 180 dias, roda diariamente de madrugada.

## Frontend — `dashboard/web`

### Rota nova

`app/src/routes/blog/metricas.tsx` (ou onde já fica a gestão do blog dentro do
dashboard) — busca dados via os endpoints acima.

### Grid de cards

- `react-grid-layout` (drag + resize com reflow automático — empurra cards
  vizinhos, sem sobrepor).
- Layout (posição/tamanho de cada card) persistido em `localStorage`, chave por
  usuário logado (`blog-metricas-layout:<userId>`) pra não misturar arranjos se
  mais de uma pessoa usar o mesmo navegador/perfil do SO.
- Cards padrão no primeiro carregamento (sem layout salvo ainda): 4 cards de
  resumo (Pageviews, Visitantes, Cliques CTA, Conversão) + série temporal +
  Top posts + Referrers + UTM source + Dispositivo/País — mesmo conjunto do
  mockup validado com o usuário.

### Cada card

- Header: título + ícone de contexto + menu "..." (sempre visível, sutil).
- Alça de resize: aparece só no `:hover` do card (canto inferior direito).
- Menu "...": **Ver como** (Lista / Proporção / Tendência — só nos cards de
  ranking, não nos cards de resumo numérico), **Exportar CSV** (desabilitado,
  "em breve" — fora do MVP), **Duplicar**, **Remover** (some da grid; volta se
  o usuário limpar o `localStorage` ou resetar o layout).
- Filtro de período próprio (select: 7d / 30d / 90d / personalizado) — dispara
  fetch independente pro endpoint daquele card.
- Gráficos (sparkline nos cards de resumo, linha/barra na série temporal) via
  **Recharts**, com animação de entrada padrão da lib.

## Segurança

- Beacon público: nenhum dado pessoal identificável — `visitor_id`/`session_id`
  são só tokens aleatórios do cliente, sem cruzar com `users.id`. Sem cookie de
  terceiro, sem fingerprinting além de UA/IP-país (mesmo nível do loja-3d).
- Rate limit obrigatório no beacon (evita flood inflando métrica ou DoS na
  tabela).
- Endpoints admin atrás de `authGuard` + permissão — nunca públicos.

## Testes

- `apps/api-go`: unitário do parser de UA (device/browser/os), unitário de cada
  query sqlc de agregação (contra Postgres real, padrão do repo), teste de rate
  limit do endpoint de ingestão.
- `dashboard/web`: teste do parser/serializer do layout do `localStorage`
  (round-trip), teste de que cada card dispara fetch com o período próprio
  (não o de outro card).

## Fora de escopo (registrar como pendência, não implementar agora)

- Funil de leitura (viu → rolou → terminou).
- Web Vitals do blog.
- Feed ao vivo / contador online.
- Exportação CSV funcional.
- Sincronizar layout customizado entre dispositivos (exigiria mover a
  persistência pro backend).
