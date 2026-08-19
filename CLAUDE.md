# Santos Tech Infra

## ⚠️ OBRIGATÓRIO — verificar build e lint antes de commitar/pushar/deployar

**NUNCA** commite, faça push ou dispare deploy sem antes rodar a verificação de **build**
e **lint** e confirmar que passam **sem erros**. Build quebrado = deploy quebrado.

- **Frontend (React/Vite):** `bun run lint` **e** `bun run build` (o build faz o type-check `tsc`).
- **Go:** `gofmt -l .` (saída vazia) · `go vet ./...` · `go build ./...` · `go test ./...`.

Se qualquer etapa falhar, **corrija antes de prosseguir** — não pushe "pra ver se passa no CI".


Auth central da **santos-tech.com**. Ponto único de login para todos os subdomínios
(`*.santos-tech.com`): emite JWT (HS256) em cookies `httpOnly` compartilhados no
domínio `.santos-tech.com`, e qualquer serviço do ecossistema (portal, painel de
email, etc.) valida o **mesmo** token.

> **Nota histórica:** o projeto nasceu como monólito Bun + Fastify (`apps/api`).
> O auth foi reescrito em **Go** (`apps/api-go`) e a API TS removida. O diretório
> `apps/api` e os pacotes TS legados em `packages/` (`env`, `contracts`) foram
> removidos na limpeza de estrutura.

## Estrutura do repositório

```
apps/
  api-go/      ← API de auth em Go (serviço principal — deploy api.santos-tech.com)
  agent-go/    ← orquestra o Claude Code em container (api.santos-tech.com/claude) — ver apps/agent-go/CLAUDE.md
  mcp-go/      ← servidor MCP (Streamable HTTP) — gateway das APIs p/ clientes MCP (api.santos-tech.com/mcp)
  auth-web/    ← Frontend de login (React 19 + Vite + TanStack Router/Query + Tailwind 4)
  hour-timer-app/ ← App desktop (Tauri v2 + React) pros PCs do laboratório: exibe o
                   cronômetro da sessão de horas do cliente (ver api-go, domínio
                   hour-sessions). Pareamento por link colado (sem cadastro manual
                   de máquina) — cada instalação gera um device_uuid sozinha e
                   manda heartbeat pra /public/lab-devices/heartbeat; identificação
                   (nome atribuído pelo admin) e controle (despairar remoto, aviso
                   na tela) ficam em /hour-lab-devices, visão em
                   dashboard/web:/admin/horas/dispositivos. Sem deploy no Coolify —
                   instalador Windows gerado via `cargo tauri build`, distribuído
                   manualmente pros PCs.
  santos-hub/  ← App desktop (Tauri v2 + React) "central de downloads" pros PCs da
                   empresa: lista o catálogo (GET /public/downloads, sem login) e
                   baixa/abre cada item no app padrão do Windows (instalador do
                   controle de máquina, scripts internos, etc. — ver api-go,
                   handlers_downloads.go). Sem tray/estado persistido (diferente do
                   hour-timer-app) — é só um launcher, abre/fecha. Cadastro/edição
                   do catálogo é admin-only em /auth/admin/downloads (upload
                   presigned pro R2 pra kind=file, ou kind=link pra URL externa).
                   Sem deploy no Coolify — instalador Windows gerado via
                   `cargo tauri build`, distribuído manualmente pros PCs.
infra/
  docker-compose.yml      ← Postgres 16 + Redis 7 + API + agent-go + mcp-go
  Dockerfile.api-go
  Dockerfile.agent-go
  Dockerfile.auth-web
  Dockerfile.mcp-go
docs/
  openapi.yaml            ← contrato OpenAPI 3.1 da Auth API (fonte de verdade dos endpoints)
```

## ⚠️ Monorepo na Coolify — `watch_paths` ao adicionar um app novo

Este repo é um **monorepo**: vários apps da Coolify apontam para ele. Sem `watch_paths`,
**qualquer push redeploya TODOS os apps** (mesmo os que não mudaram). Cada app tem um
`watch_paths` que restringe o auto-deploy ao seu próprio diretório.

**Ao criar um app novo aqui, configure o `watch_paths` dele na hora.** Via API da Coolify
(`PATCH /api/v1/applications/{uuid}`), padrão um glob por linha (`\n`):

```
apps/<novo-app>/**
infra/Dockerfile.<novo-app>
```

Inclua também todo arquivo **compartilhado** que o build do app copia (ex.: `api-go` e
`mcp-go` adicionam `docs/openapi.yaml`). Confira o que já existe:

```bash
# token e base URL: ver memória coolify-infra-apps / easypanel-api
curl -s -H "Authorization: Bearer $COOLIFY_TOKEN" \
  "$COOLIFY_API_URL/applications/$UUID" | jq -r .watch_paths
```

**Caveat:** arquivo compartilhado fora de qualquer `watch_paths` (ex.: algo na raiz) não
dispara auto-deploy — nesse caso, deploy manual do app afetado.

## Stack (Auth API — `apps/api-go`)

| Camada | Tecnologia |
|--------|-----------|
| Linguagem | Go 1.25 |
| HTTP | `net/http` stdlib (sem framework; mux com padrões `"POST /rota"`) |
| Banco | PostgreSQL 16 via `pgx/v5` |
| Cache / rate limit / tokens efêmeros | Redis 7 via `go-redis/v9` |
| JWT | `golang-jwt/v5` (HS256) |
| Senhas | `argon2id` (compatível com `Bun.password` da versão TS) |
| OAuth | `golang.org/x/oauth2` (Google) |
| MFA | `pquerna/otp` (TOTP) + OTP por email + recovery codes |
| Logs | `slog` estruturado em stdout |

Arquivos (flat, `package main`): `main.go`, `config.go`, `server.go` (CORS, authGuard,
cookies), `routes.go`, `handlers_{auth,mfa,password,oauth}.go`, `db.go` (pgx + migração
MFA embutida), `redis.go`, `token.go` (JWT), `password.go` (argon2), `ratelimit.go`,
`email.go` (cliente da nossa API de emails), `models.go`, `errors.go`, `util.go`.

## Rodar

```bash
# Tudo (API + Postgres + Redis) via Docker
docker compose -f infra/docker-compose.yml up --build      # ou: bun run dev

# Só os bancos, rodando a API local
docker compose -f infra/docker-compose.yml up -d postgres redis
cd apps/api-go && go run .                                  # ou: bun run dev:auth (da raiz)

# Frontend de login
cd apps/auth-web && bun run dev
```

A migração do schema MFA roda **automaticamente** no boot (`migrate()` em `db.go`,
idempotente). As tabelas base (`users`, `sessions`, `oauth_accounts`, `custom_roles`)
já existem no Postgres compartilhado e **não** são criadas aqui.

## Variáveis de ambiente

`apps/api-go/.env.example` é a referência. Obrigatórias (sem default → boot falha):

| Variável | Descrição |
|----------|-----------|
| `DATABASE_URL` | PostgreSQL (mesmo banco do ecossistema) |
| `REDIS_URL` | Redis (sessões efêmeras, OTP, rate limit) |
| `JWT_SECRET` | Secret dos access tokens — **igual** ao dos outros serviços |
| `JWT_REFRESH_SECRET` | Secret dos refresh tokens |
| `EMAIL_API_KEY` | Chave da nossa API de emails (`mails.santos-tech.com`) |

Opcionais (com default): `PORT` (3333), `NODE_ENV`, `COOKIE_DOMAIN` (ex: `.santos-tech.com`),
`CORS_ORIGIN` (CSV de origens), `AUTH_WEB_ORIGIN`, `GOOGLE_CLIENT_ID`/`SECRET`/`CALLBACK_URL`,
`EMAIL_API_URL`, `API_VAULT_SECRET` (vazio = roteador de chaves de API desabilitado, 503).

## API / Endpoints

Contrato completo (request/response, erros, rate limits): **`docs/openapi.yaml`**.
Cole-o em qualquer viewer Swagger/Redoc para navegar.

| Método | Rota | Auth | Rate limit |
|--------|------|------|-----------|
| GET  | `/health` | — | global |
| POST | `/auth/register` | — | 5/min |
| POST | `/auth/login` | — | 10/min |
| POST | `/auth/logout` | cookie | global |
| GET  | `/auth/logout` | — | global |
| GET  | `/auth/me` | cookie/Bearer | global |
| POST | `/auth/refresh` | refresh cookie | 20/min |
| POST | `/auth/forgot-password` | — | 3/5min |
| POST | `/auth/reset-password` | — | 10/min |
| POST | `/auth/mfa/setup` | sessão | global |
| POST | `/auth/mfa/enable` | sessão | global |
| POST | `/auth/mfa/disable` | sessão | global |
| POST | `/auth/mfa/email` | — (challenge) | 3/5min |
| POST | `/auth/mfa/verify` | — (challenge) | 10/min |
| GET  | `/auth/google` | — | global |
| GET  | `/auth/google/callback` | — | global |

### Convenções

- **Erros:** sempre `{ "code", "message" }` + status HTTP. `message` em português.
- **Cookies:** `access_token` (2h) e `refresh_token` (7d), `httpOnly`, `SameSite=Lax`,
  `Domain=COOKIE_DOMAIN`, `Secure` em produção. Refresh **rotaciona** a sessão.
- **Auth:** access token via cookie `access_token` **ou** `Authorization: Bearer <token>`.
- **Papéis:** `1=Student, 2=Teacher, 3=Admin, 4=Custom` (permissões de `custom_roles.permissions` jsonb resolvidas em `/auth/me`).
- **Rate limit:** por `(rota + IP)` no Redis, *fail-open*; global de 200/min/IP. IP via
  `CF-Connecting-IP` → `X-Forwarded-For` → `RemoteAddr` (atrás de Cloudflare + Traefik).
- **MFA:** login com MFA devolve `{ mfaRequired, challenge }` (10min); 2º passo em
  `/auth/mfa/verify` aceita TOTP, OTP por email ou recovery code.
- **Emails** (reset, OTP) saem pela nossa API (`POST {EMAIL_API_URL}/send`), não Resend.

## Logs de acesso (middleware comum)

Todos os serviços Go (`api-go`, `agent-go`, `bot-go`, `mcp-go`, `payments-go`,
`auto-fixer`) compartilham um middleware em `logging.go` (arquivo **idêntico**,
`package main`), plugado como camada **mais externa** do handler raiz
(`requestLogger(...)`). Cada requisição vira uma linha JSON no stdout → coletada pelo
Grafana Alloy → **Loki** (Grafana em `grafana.santos-tech.com`).

**Campos:** `time`, `level` (5xx=ERROR · 4xx=WARN · `/health`=DEBUG · resto=INFO),
`request_id` (gerado ou propagado de `X-Request-Id`/`CF-Ray`; devolvido no header de
resposta e disponível nos handlers via `requestIDFromContext(ctx)`), `ip`
(`CF-Connecting-IP` → `X-Forwarded-For` → `RemoteAddr`), `method`, `path`, `query`,
`status`, `bytes`, `dur_ms`, `ua`, `proto`, `host`, `req_body`, `resp_body`.

**Payload:** request e response são capturados por inteiro. Valores sensíveis (senha,
token, secret, cartão, cvv…) são **redigidos para `***`**. Uploads (multipart) e tipos
binários não são capturados (só placeholder); **SSE e WebSocket são preservados**
(`Flush`/`Hijack` delegados). Panics em qualquer camada são recuperados (loga stack +
responde 500).

**Configuração por env** (default entre parênteses):

| Var | Default | Efeito |
|-----|---------|--------|
| `LOG_LEVEL` | `info` | nível mínimo: `debug`/`info`/`warn`/`error` |
| `LOG_BODIES` | `1` | captura request+response (`0` desliga, vira access-log puro) |
| `LOG_REDACT` | `1` | redige credenciais — **`0` loga cru, inclui senhas (não recomendado: LGPD)** |
| `LOG_BODY_MAX_BYTES` | `16384` | bytes logados por payload (resto truncado) |
| `LOG_BODY_HARD_CAP` | `1048576` | acima disso o request nem é bufferizado (anti-OOM) |

`bot-go` e `auto-fixer` já configuravam o `slog`; os demais chamam `initLogging()` no
`main`. **Ao criar um serviço Go novo:** copie `logging.go`, plugue `requestLogger(...)`
no handler raiz e chame `initLogging()` no `main`.

## ⚠️ Padrões OBRIGATÓRIOS ao criar/alterar um serviço Go

Todo serviço Go do repo (`api-go`, `agent-go`, `bot-go`, `mcp-go`, `payments-go`,
`auto-fixer`, e qualquer novo) **deve** seguir os padrões abaixo. Não são opcionais:
ao criar um serviço novo ou alterar um existente, garanta que todos estão presentes.

1. **Graceful shutdown + timeouts no `http.Server`.** Nada de `http.ListenAndServe`
   pelado. Construa um `&http.Server{}` com `ReadHeaderTimeout`, `ReadTimeout`,
   `WriteTimeout` e `IdleTimeout` definidos (anti slow-loris / conexão pendurada).
   No `main`, escute `SIGINT`/`SIGTERM` (`signal.NotifyContext`) e, ao receber,
   chame `srv.Shutdown(ctx)` com timeout — drena requisições em voo, fecha o pool
   do Postgres e o cliente Redis antes de sair. (SSE/WebSocket de longa duração:
   feche-os explicitamente no shutdown.)

2. **`/ready` distinto de `/health`.**
   - `/health` (ou `/healthz`): **liveness** — responde 200 se o processo está vivo,
     sem tocar dependências. É o que já existe e o que o log marca como `DEBUG`.
   - `/ready` (readiness): **checa as dependências** (Postgres `Ping`, Redis `Ping`,
     e o que mais for crítico) e só devolve 200 se tudo responde; 503 caso contrário.
     É o endpoint que o orquestrador usa para decidir mandar tráfego.

3. **`/metrics` Prometheus.** Exponha métricas no formato Prometheus em `/metrics`
   (via `prometheus/client_golang` + `promhttp.Handler()`). No mínimo: latência e
   contagem de requests HTTP por rota/status, e métricas do pool do Postgres. O
   scrape é feito pela stack de observabilidade (ver memória
   `observabilidade-wireguard-stack`).

4. **Chaves de rate-limit prefixadas pelo nome do serviço.** O rate limit é no Redis,
   compartilhado entre todos os serviços. Para não colidir entre serviços, **toda
   chave de rate limit deve ser prefixada pelo nome do serviço** (ex.:
   `api-go:rl:<rota>:<ip>`, `payments-go:rl:...`). Nunca use uma chave global sem
   namespace — dois serviços com a mesma rota dividiriam o mesmo balde.

5. **CI Go em `.github/workflows/go.yml`.** O pipeline de CI já roda `gofmt -l`,
   `go vet`, `go build` e `go test` para os serviços Go. **Ao adicionar um serviço Go
   novo, inclua-o na matriz do `go.yml`** para que ele passe pela mesma verificação.
   (O gate local de pré-commit continua valendo — ver topo deste arquivo.)

6. **sqlc para acesso a banco.** Todo SQL fica em arquivos `.sql` versionados — nunca
   escreva `pool.Query(...)` / `pool.Exec(...)` / `pool.QueryRow(...)` inline em handlers.
   Estrutura padrão por serviço:
   - `db/schema.sql` — schema completo das tabelas usadas pelo serviço
   - `db/query/*.sql` — queries anotadas (`-- name: X :one` / `:many` / `:exec` / `:execrows`)
   - `db/` — código gerado pelo sqlc (`package db`, `sql_driver: "pgx/v5"`)
   - `sqlc.yaml` na raiz do serviço
   Callers: `q := db.New(pool)` → `q.SomeQuery(ctx, ...)`. Transações: `db.New(tx)`.
   Geração: `/home/guilherme/.local/bin/sqlc generate` (rodar na raiz do serviço).
   Compatibilidade PgBouncer (`QueryExecModeExec`) é transparente — o pool já trata.

Endpoints operacionais (`/health`, `/ready`, `/metrics`) ficam **fora** de auth e fora
do rate limit, e o `/health`/`/ready` não devem logar como `INFO` (ver tabela de logs).

## Testes

```bash
cd apps/api-go && go test ./...
```

A maioria é unitária (`httptest`, sem dependências). Os blocos de integração esperam
Postgres + Redis reais acessíveis — suba `docker compose ... up -d postgres redis` antes.

## Pré-commit

Antes de **todo** commit, rode e garanta que passa:

**API Go (`apps/api-go`):**
```bash
cd apps/api-go
gofmt -l .        # não deve listar NENHUM arquivo (se listar: gofmt -w .)
go vet ./...
go build ./...
go test ./...     # integração precisa de Postgres+Redis
```

**Bot Go (`apps/bot-go`):**
```bash
# go está em ~/.local/bin — use o prefixo PATH abaixo
bash -c "cd apps/bot-go && PATH=\$PATH:\$HOME/.local/bin gofmt -l ."   # deve ser vazio
bash -c "cd apps/bot-go && PATH=\$PATH:\$HOME/.local/bin gofmt -w ."   # formata
bash -c "cd apps/bot-go && PATH=\$PATH:\$HOME/.local/bin go vet ."
bash -c "cd apps/bot-go && PATH=\$PATH:\$HOME/.local/bin go build ."
```

**Frontend (`apps/auth-web`):**
```bash
cd apps/auth-web
bun run build     # type-check (tsc --noEmit) + build de produção
```

**Revisão do código (checklist):**
- [ ] Releia o próprio diff: `git diff` e `git diff --staged`.
- [ ] Rode `/code-review` (Claude Code) para revisão automatizada de bugs e qualidade.
- [ ] Mexeu em auth, JWT, cookies, sessão ou rate limit? Rode também `/security-review`.
- [ ] Alterou rotas/handlers (`routes.go`, `handlers_*`)? Atualize `docs/openapi.yaml` no mesmo commit.
- [ ] Sem segredos no commit (`.env`, chaves) — confira `git status` e o diff.
- [ ] Mensagem de commit clara, no imperativo e com escopo (ex.: `feat(api-go): ...`).

## Documentação OpenAPI

`docs/openapi.yaml` é a **fonte de verdade** dos endpoints. Ao adicionar/alterar uma
rota em `apps/api-go/routes.go` ou nos handlers, atualize o YAML no mesmo PR.

## Documentação central (llms.txt)

`apps/api-go/llms.txt` é a documentação viva de **todas** as APIs do ecossistema
(auth, claude agent, email, e o que vier), servida autenticada em
`https://api.santos-tech.com/llms.txt` (sessão do auth ou PAT `st_...`).

**Regra: terminou uma feature que muda rotas/contratos de QUALQUER API — atualize o
`llms.txt` no mesmo conjunto de mudanças.** Para APIs deste repo (api-go, agent-go),
no mesmo commit; para APIs de outros repos (ex.: email), commit aqui acompanhando.

## Fallbacks — sem ferramenta ou credencial?

Agentes (Claude) devem **degradar com graça: nunca travar, nunca inventar valores,
nunca fingir que deu certo**. Cadeia padrão quando algo falta:

1. **Credenciais do ecossistema** (`~/.config/santos-tech/claude.env` → `ST_PAT`,
   conta de serviço): se o arquivo não existir nesta máquina, **pergunte ao usuário**
   como autenticar — não chute tokens, não siga sem auth.
2. **Doc das APIs** (`api.santos-tech.com/llms.txt`, autenticada): sem credencial ou
   sem rede, caia para o `docs/openapi.yaml` local e o CLAUDE.md; se ainda faltar o
   contrato, **pergunte ao usuário** em vez de adivinhar endpoint/payload.
3. **`gh` CLI sem permissão/escopo** (secrets, org): mostre ao usuário o comando
   exato para ele rodar (`!` no prompt ou terminal) em vez de tentar contornar.
4. **Banco/Redis de produção**: sem acesso (túnel/chave SSH), use o caminho local
   (`DEV_AUTH=1` + Postgres local) ou peça o acesso — não desligue checagens nem
   aponte para hosts inventados.
5. **Deploy/infra inacessível**: descreva o passo exato que o usuário precisa fazer
   (ex.: botão Deploy no Easypanel) e o que verificar depois.

Em todos os casos: **diga claramente o que falta, o que você tentou e o que precisa
do usuário** — uma pergunta objetiva vale mais que uma suposição silenciosa.

## Identidade Visual

**Paleta principal:**
- Azul principal: `#187ABF` / `#338FBF` / `#49A8EB`
- Verde-teal (CTA, botões, destaque): `#0DB88F`
- Azul-marinho (fundo escuro, títulos): `#0E2937` / `#212D3A`
- Fundo: `#F5F8FA` / `#FFFFFF`
- Texto: `#212121`

**Paleta de apoio:**
- Azul interação/hover: `#0067BE`
- Azul acinzentado: `#496B84`

**Por programa:**
- CREATE: `#0067BE` | JR: `#512374` | CAMPS: `#1C8299` | ACADEMIES: `#0411A0`

**Tipografia:** sans-serif, bold nos títulos, regular no corpo. Moderna, limpa, tecnológica.

**Estilo:** moderno, tecnológico, organizado. Cards arredondados, glow sutil, gradientes leves, poucos elementos bem escolhidos.
