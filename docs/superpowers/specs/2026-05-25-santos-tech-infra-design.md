# Santos Tech Infra — Design Spec
*Data: 2026-05-25*

## Contexto

Infraestrutura centralizada do santos-tech.com para reutilização em todos os projetos educacionais. O objetivo é eliminar duplicação de código de auth, sessão e lógica central entre projetos como `portal-do-aluno` e futuros sistemas educacionais.

## Decisões Técnicas

| Decisão | Escolha | Motivo |
|---------|---------|--------|
| Runtime | Bun | Já usado nos projetos existentes |
| Framework API | Fastify | Já usado nos projetos existentes |
| Linguagem | TypeScript | Ecossistema unificado com frontends |
| ORM | Drizzle | Type-safe, leve, compatível com Bun |
| Banco principal | PostgreSQL | Dados relacionais (usuários, sessões) |
| Banco de logs | MongoDB | Logs de request em formato livre |
| Sessão | httpOnly cookies (JWT) | Segurança, compatibilidade cross-origin |

## Arquitetura

Monólito modular — uma única API Fastify que serve todos os frontends educacionais. Módulos isolados internamente, sem overhead de microserviços. Quando necessário, um módulo pode ser extraído como serviço independente sem refactor dos frontends.

```
santos-tech-infra/
├── apps/
│   └── api/
│       ├── src/
│       │   ├── modules/
│       │   │   └── auth/
│       │   │       ├── auth.routes.ts
│       │   │       ├── auth.handler.ts
│       │   │       ├── auth.schema.ts
│       │   │       └── auth.service.ts
│       │   ├── shared/
│       │   │   ├── middleware/     ← authGuard, cors, rateLimit
│       │   │   ├── errors/         ← AppError, errorHandler
│       │   │   └── logger/         ← Pino → MongoDB
│       │   ├── db/
│       │   │   ├── client.ts       ← Drizzle + PostgreSQL
│       │   │   ├── mongo.ts        ← MongoDB client
│       │   │   └── schema/
│       │   │       ├── users.ts
│       │   │       ├── oauth.ts
│       │   │       └── sessions.ts
│       │   └── app.ts              ← bootstrap Fastify
│       └── package.json
├── packages/
│   ├── contracts/                  ← tipos TS para frontends
│   │   └── src/
│   │       ├── user.ts
│   │       ├── auth.ts
│   │       └── errors.ts
│   └── env/                        ← validação Zod de env vars
│       └── src/
│           └── api.ts
├── infra/
│   └── docker-compose.yml
├── CLAUDE.md
└── package.json                    ← workspace Bun
```

## Módulo Auth

### Endpoints

```
POST /auth/register          → cria conta com email + senha
POST /auth/login             → autentica, seta cookies httpOnly
POST /auth/logout            → limpa cookies
GET  /auth/me                → retorna usuário autenticado
POST /auth/refresh           → renova access_token via refresh_token
GET  /auth/google            → inicia fluxo OAuth Google
GET  /auth/google/callback   → callback OAuth — só aceita emails já cadastrados
```

### Fluxo de Sessão

- Login bem-sucedido gera dois JWTs em cookies `httpOnly; Secure; SameSite=Lax`:
  - `access_token`: expira em 15 minutos
  - `refresh_token`: expira em 7 dias
- `GET /auth/me` valida o `access_token` do cookie
- `POST /auth/refresh` valida o `refresh_token`, gera novo par de tokens
- OAuth Google: verifica se o email do Google já existe em `users` → loga, **não cria conta nova**

### Schema PostgreSQL (Drizzle)

```sql
users (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email         text UNIQUE NOT NULL,
  name          text NOT NULL,
  password_hash text,           -- null para contas OAuth-only
  created_at    timestamp DEFAULT now()
)

oauth_accounts (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id     uuid REFERENCES users(id) ON DELETE CASCADE,
  provider    text NOT NULL,    -- 'google'
  provider_id text NOT NULL,
  created_at  timestamp DEFAULT now(),
  UNIQUE (provider, provider_id)
)

sessions (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id            uuid REFERENCES users(id) ON DELETE CASCADE,
  refresh_token_hash text NOT NULL,
  expires_at         timestamp NOT NULL,
  created_at         timestamp DEFAULT now()
)
```

## Logs (MongoDB)

Cada request é logado via Pino com transport customizado para MongoDB:

```typescript
{
  method: 'POST',
  path: '/auth/login',
  status: 200,
  user_id: 'uuid | null',
  ip: '0.0.0.0',
  duration_ms: 42,
  timestamp: ISODate
}
```

## Pacote `contracts`

Tipos TypeScript sem lógica, consumidos pelos frontends:

```typescript
export type User = {
  id: string
  email: string
  name: string
  createdAt: string
}

export type AuthResponse = { user: User }
export type ApiError = { code: string; message: string }
```

Instalado nos frontends via workspace Bun: `"@santos-tech/contracts": "workspace:*"`

## Pacote `env`

Validação de variáveis de ambiente com Zod — a API não sobe se alguma var estiver faltando:

```typescript
// Variáveis necessárias:
DATABASE_URL, MONGO_URL,
JWT_SECRET, JWT_REFRESH_SECRET,
GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET,
COOKIE_DOMAIN, PORT
```

## Infra Docker

```yaml
services:
  api:
    build: ../apps/api
    ports: ["3000:3000"]
    depends_on: [postgres, mongo]
    env_file: ../.env

  postgres:
    image: postgres:16-alpine
    volumes: [postgres_data:/var/lib/postgresql/data]

  mongo:
    image: mongo:7
    volumes: [mongo_data:/data/db]
```

Sem separação dev/prod — API sempre conecta nos bancos rodando via docker-compose.

## CLAUDE.md

Documentará:
- Stack e estrutura de pastas
- Como adicionar um novo módulo
- Convenções de código (erros, logs, validação)
- Variáveis de ambiente necessárias
- Paleta de cores santos-tech.com para consistência visual

## Verificação

1. `docker compose up` sobe postgres + mongo + api sem erros
2. `POST /auth/register` cria usuário no PostgreSQL
3. `POST /auth/login` retorna cookies httpOnly
4. `GET /auth/me` retorna usuário com cookie válido
5. `GET /auth/google` redireciona para Google
6. Callback Google com email inexistente retorna erro 401
7. Callback Google com email existente seta cookies e retorna usuário
8. Logs aparecem no MongoDB após cada request
9. Frontend pode importar `@santos-tech/contracts` e usar os tipos
