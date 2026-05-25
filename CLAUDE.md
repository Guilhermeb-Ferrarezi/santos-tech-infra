# Santos Tech Infra

API central da santos-tech.com. Monólito modular em Bun + Fastify servindo todos os projetos educacionais.

## Stack

| Camada | Tecnologia |
|--------|-----------|
| Runtime | Bun |
| Framework | Fastify 5 |
| Linguagem | TypeScript strict |
| ORM | Drizzle + PostgreSQL 16 |
| Logs | MongoDB 7 |
| Auth | JWT (jose) + cookies httpOnly |
| Validação env | Zod (`@santos-tech/env`) |

## Estrutura

```
apps/api/src/
  modules/        ← um diretório por domínio (auth, payments, ...)
  shared/         ← errors, logger, middleware
  db/             ← Drizzle client, schema, migrate
packages/
  env/            ← validação de variáveis de ambiente
  contracts/      ← tipos TypeScript compartilhados com frontends
infra/
  docker-compose.yml
  Dockerfile
```

## Rodar

```bash
# Subir bancos e API
docker compose -f infra/docker-compose.yml up -d

# Rodar migrations (primeira vez ou após alterar schema)
cd apps/api && bun run db:migrate

# Modo desenvolvimento (sem Docker para a API)
docker compose -f infra/docker-compose.yml up -d postgres mongo
bun run dev
```

## Variáveis de Ambiente

Copie `.env.example` para `.env` e preencha:

| Variável | Descrição |
|----------|-----------|
| `DATABASE_URL` | PostgreSQL connection string |
| `MONGO_URL` | MongoDB connection string |
| `JWT_SECRET` | Secret para access tokens (≥16 chars) |
| `JWT_REFRESH_SECRET` | Secret para refresh tokens (≥16 chars) |
| `GOOGLE_CLIENT_ID` | OAuth Google Client ID |
| `GOOGLE_CLIENT_SECRET` | OAuth Google Client Secret |
| `GOOGLE_CALLBACK_URL` | URL de callback OAuth (ex: https://api.santos-tech.com/auth/google/callback) |
| `COOKIE_DOMAIN` | Domínio dos cookies (ex: .santos-tech.com) |
| `CORS_ORIGIN` | Origin permitida — URL do frontend |
| `PORT` | Porta da API (padrão: 3000) |

## Adicionar um Novo Módulo

1. Crie `apps/api/src/modules/<nome>/`
2. Estrutura obrigatória:
   - `<nome>.routes.ts` — plugin Fastify com `export async function <nome>Routes(app)`
   - `<nome>.handler.ts` — handlers das rotas
   - `<nome>.service.ts` — lógica de negócio
   - `<nome>.schema.ts` — JSON Schema de validação
3. Registre no `apps/api/src/app.ts`:
   ```typescript
   await app.register(<nome>Routes, { prefix: '/<nome>' })
   ```
4. Exporte novos tipos em `packages/contracts/src/index.ts`
5. Adicione variáveis de ambiente em `packages/env/src/index.ts` se necessário

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

## Testes

```bash
# Todos os testes
bun test

# Módulo específico
bun test apps/api/src/modules/auth/

# Com watch
bun test --watch
```

Os testes de integração (auth.handler.test.ts) requerem PostgreSQL rodando. Suba os bancos antes:
```bash
docker compose -f infra/docker-compose.yml up -d postgres mongo
```

## Migrations

```bash
# Gerar migration após alterar schema Drizzle
cd apps/api && bun run db:generate

# Aplicar migrations no banco
bun run db:migrate
```
