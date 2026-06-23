# Cron jobs programáveis — design

**Data:** 2026-06-23
**Status:** aprovado (brainstorming) — pronto para plano de implementação

## Problema

Hoje o agendamento no ecossistema é ad-hoc e espalhado:

- **`payments-go`** usa loops `time.Ticker` chumbados no código (`runRecurringLoop`
  24h, `runExpiryLoop` 1h). Mudar horário exige redeploy.
- **`bot-go`** tem uma tabela `scheduled_contacts` + worker com polling
  (`FOR UPDATE SKIP LOCKED`), mas é específico de follow-ups do bot.

Cada serviço reinventa agendamento. Queremos **cron jobs programáveis em runtime**:
um admin cria, edita, pausa e acompanha jobs **sem deploy**, e qualquer serviço do
ecossistema pode ser acionado por eles.

## Decisões (resultado do brainstorming)

| Tema | Decisão |
|------|---------|
| Quem programa | Feature de produto/admin — humanos via UI, em runtime, sem deploy. |
| O que o job faz | **HTTP genérico** (método + URL + headers + body) com um **catálogo curado** de ações por cima. URL livre é escape hatch. |
| Onde mora o engine | **Serviço Go novo `cron-go`** neste repo (`santos-tech-infra`), dono de todo o domínio. |
| Onde mora a UI | Repo `../org/dashboard` (`web/`), área `/admin/agendamentos`. |
| Horário | Cron expression internamente; UI com presets + campo cron avançado. Fuso fixo `America/Sao_Paulo` → convertido p/ UTC. |
| Falha | Retry com backoff até `max_retries`, depois `failed`. |
| Sobreposição | **Skip** se execução anterior ainda `running`. |
| Horário perdido (downtime) | **Sem catch-up** — no máximo 1 disparo ao voltar. Jobs devem ser idempotentes. |
| Disparo manual | Botão "Rodar agora" (`POST /cron/jobs/{id}/run`). |

## Arquitetura — fronteira entre repos

- **`cron-go`** (novo serviço em `santos-tech-infra`) — **dono único** do domínio cron:
  tabela de jobs, histórico de execução, API REST (CRUD + histórico + disparo manual),
  scheduler (loop que acorda no horário) e dispatcher (bate no endpoint-alvo). Deploy
  próprio na Coolify (ex.: `api.santos-tech.com/cron`).
- **`dashboard/web`** (repo `../org/dashboard`) — só a UI admin (`/admin/agendamentos`),
  cliente TanStack Query da API do `cron-go`. Zero lógica de agendamento.
- O `dashboard/api` (Go próprio) **não** participa — para não fatiar o domínio cron em
  dois bancos/repos.

## Dois contextos de auth

1. **Dashboard → API do `cron-go`:** sessão do admin (cookie JWT compartilhado em
   `.santos-tech.com`) + checagem de papel **Admin**.
2. **`cron-go` (dispatcher) → serviço-alvo:** PAT de **conta de serviço dedicada e de
   menor privilégio** (Bearer), guardado nos secrets da Coolify, rotacionável. Cada alvo
   valida com o `authGuard` que já tem.

## Modelo de dados (sqlc, Postgres compartilhado)

**`cron_jobs`**: `id`, `name`, `description`, `schedule_cron` (texto), `timezone`
(`America/Sao_Paulo`), `enabled` (bool — pausar = `false`), `action_kind`
(`catalog` | `http`), `action_ref` (id da ação do catálogo) **ou**
`http_method`/`http_url`/`http_headers`/`http_body`, `timeout_secs`, `max_retries`,
`next_run_at` (UTC, calculado), `last_run_at`, `created_by`, timestamps.

**`cron_runs`** (histórico append-only): `id`, `job_id`, `started_at`, `finished_at`,
`status` (`success`|`failed`|`skipped_overlap`|`running`), `attempt`, `http_status`,
`response_excerpt` (truncado + redigido), `error`.

## Scheduler + dispatcher (dentro do `cron-go`)

- **Tick loop** acorda ~30s, busca `enabled=true AND next_run_at <= now()` com
  **`FOR UPDATE SKIP LOCKED`** (padrão do `bot-go`) — seguro com >1 réplica, sem
  disparo duplicado.
- **`next_run_at`**: parser de cron expression (`robfig/cron/v3`), do fuso do job p/ UTC.
- **Confiabilidade**: retry com backoff até `max_retries`; **skip** em sobreposição;
  **sem catch-up** de horários perdidos.
- **Dispatcher**: monta o request (catálogo → endpoint fixo; ou HTTP cru), injeta o
  Bearer da conta de serviço, aplica `timeout_secs`, **valida a allowlist de hosts**
  (só ecossistema; bloqueia localhost / IP privado / link-local / metadata cloud),
  grava o `cron_run`.

## Catálogo de ações (curado, em código)

Registro Go (`map[string]CatalogAction`) — cada item `{ id, label, método, path/host
alvo, schema de params }`. Ex.: `payments.gerar-cobrancas-mes`,
`email.relatorio-semanal`, `auth.reenviar-convites-pendentes`. UI lê via
`GET /cron/catalog`. HTTP cru **desligado por padrão**, atrás de `CRON_ALLOW_RAW_HTTP=0`.

## API REST do `cron-go`

- `GET/POST /cron/jobs`
- `GET/PATCH/DELETE /cron/jobs/{id}`
- `POST /cron/jobs/{id}/run` (disparo manual)
- `POST /cron/jobs/{id}/pause` · `/resume`
- `GET /cron/jobs/{id}/runs` (histórico)
- `GET /cron/catalog`
- Operacionais fora de auth/rate-limit: `/health`, `/ready`, `/metrics`.

Tudo (exceto operacionais) atrás do guard de Admin.

## UI no dashboard (`/admin/agendamentos`)

- `src/lib/cron.ts` — módulo de queries TanStack (padrão dos vizinhos `payments.ts` etc.).
- Telas com `PageShell`: **lista** (nome, próxima execução, status do último run, toggle
  pausar), **form** criar/editar (presets "todo dia às HH:MM" / "toda segunda" + campo
  cron avançado + seletor de ação do catálogo), **histórico** de execuções.
- Entrada no `nav.ts`; cobertura **E2E smoke + screenshot** (obrigatório no repo) com
  mocks dos endpoints novos.

## Padrões obrigatórios de serviço Go (checklist CLAUDE.md)

`cron-go` nasce com: graceful shutdown + timeouts no `http.Server`; `/health` vs `/ready`
(pinga Postgres); `/metrics` Prometheus; rate-limit prefixado (`cron-go:rl:...`);
`logging.go` copiado; sqlc (`db/schema.sql`, `db/query/*.sql`, `sqlc.yaml`);
`Dockerfile.cron-go` + healthcheck no compose; `watch_paths` na Coolify
(`apps/cron-go/**` + `infra/Dockerfile.cron-go`); entrada na matriz do `go.yml`.

## Fora de escopo (YAGNI)

- Catch-up de execuções perdidas.
- HMAC por-target (PAT + allowlist + catálogo cobrem o risco).
- Disparo do agente Claude por job (pode virar uma `CatalogAction` no futuro, sem
  mudança de arquitetura).
