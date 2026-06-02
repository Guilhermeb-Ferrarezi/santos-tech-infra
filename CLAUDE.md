# Santos Tech Infra

Auth central da **santos-tech.com**. Ponto único de login para todos os subdomínios
(`*.santos-tech.com`): emite JWT (HS256) em cookies `httpOnly` compartilhados no
domínio `.santos-tech.com`, e qualquer serviço do ecossistema (portal, painel de
email, etc.) valida o **mesmo** token.

> **Nota histórica:** o projeto nasceu como monólito Bun + Fastify (`apps/api`).
> O auth foi reescrito em **Go** (`apps/api-go`) e a API TS removida — `apps/api`
> hoje só contém `node_modules` residual. Os pacotes TS em `packages/` são legados
> daquela fase.

## Estrutura do repositório

```
apps/
  api-go/      ← API de auth em Go (serviço principal — deploy api.santos-tech.com)
  agent-go/    ← orquestra o Claude Code em container (api.santos-tech.com/claude) — ver apps/agent-go/CLAUDE.md
  auth-web/    ← Frontend de login (React 19 + Vite + TanStack Router/Query + Tailwind 4)
  api/         ← legado, esvaziado (auth migrado 100% pro Go)
packages/
  env/         ← validação de env (Zod) — legado TS
  contracts/   ← tipos compartilhados — legado TS
infra/
  docker-compose.yml      ← Postgres 16 + Redis 7 + API + agent-go
  Dockerfile.api-go
  Dockerfile.agent-go
  Dockerfile.auth-web
docs/
  openapi.yaml            ← contrato OpenAPI 3.1 da Auth API (fonte de verdade dos endpoints)
```

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
`EMAIL_API_URL`.

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
gofmt -l .        # formatação — não deve listar NENHUM arquivo (se listar: gofmt -w .)
go vet ./...      # análise estática
go build ./...    # compila sem erros
go test ./...     # testes (ver nota acima: integração precisa de Postgres+Redis)
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
