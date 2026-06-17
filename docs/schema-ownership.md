# Schema ownership — Postgres compartilhado

O ecossistema Santos Tech roda sobre **um único Postgres compartilhado**. Vários
serviços (em repositórios e linguagens diferentes) aplicam migrações nesse mesmo
banco no boot. Este documento registra **quem é dono de cada conjunto de tabelas**,
qual o mecanismo de migração de cada um, o risco de múltiplos donos e a recomendação.

> Regra de ouro: **antes de qualquer `ALTER`/`DROP`/`RENAME` numa tabela, confira aqui
> quem é o dono.** Mexer numa tabela de outro serviço quebra o boot/migração dele.

## Mapa de propriedade

| Conjunto de tabelas | Dono | Mecanismo de migração | Repositório |
|---|---|---|---|
| `course`, `module`, `phase`, `exercise`, `answer`, `class`, e demais tabelas base do portal | **Portal .NET** | EF Core migrations (versionadas, `dotnet ef`) | outro repo (.NET) |
| `users`, `sessions`, `oauth_accounts`, `custom_roles` (tabelas base de usuário/sessão) | **Portal .NET** (criação) / **api-go** (evolução das colunas próprias) | criadas pelo .NET; `api-go` adiciona colunas/índices via `migrate()` | .NET + `apps/api-go` |
| MFA (`recovery_codes`), `api_keys`, `oauth_clients`, `boards`/`board_members`, `social_posts`* e colunas extras de `users`/`custom_roles` | **api-go** | `migrate()` inline em `apps/api-go/db.go` (string SQL idempotente, roda no boot) | `apps/api-go` |
| Tabelas do bot WhatsApp (leads, conversas, follow-up, KB, etc.) | **bot-go** | **migrations SQL versionadas** em `apps/bot-go/migrations/NNNN_*.sql`, com checksum em `schema_migrations` (drift = aborta) | `apps/bot-go` |
| Pagamentos (`pay_students`, `pay_plans`, `pay_subscriptions`, `pay_charges`, `pay_webhook_events`, ...) | **payments-go** | `migrate()` inline em `apps/payments-go/db.go` (prefixo `pay_`, idempotente) | `apps/payments-go` |
| Agente Claude (`claude_conversations`, `claude_messages`, `claude_credentials`, ...) | **agent-go** | `migrate()` inline em `apps/agent-go/db.go` (prefixo `claude_`, idempotente) | `apps/agent-go` |

\* `social_posts` e tabelas correlatas hoje vivem no `migrate()` do `api-go`.

### Convenção de prefixo

`payments-go` (`pay_`) e `agent-go` (`claude_`) namespeiam suas tabelas por prefixo —
isso evita colisão com o portal e deixa óbvio o dono pelo nome. **Novos serviços devem
seguir o mesmo padrão**: prefixe as tabelas próprias com o nome curto do serviço.

## O risco: múltiplos donos da mesma tabela

As tabelas de **usuário/sessão** (`users`, `sessions`, `custom_roles`) são o ponto
sensível: são **criadas** pelo portal .NET (EF migrations) mas **evoluídas** pelo
`api-go` (que faz `ALTER TABLE ... ADD COLUMN IF NOT EXISTS ...` no boot).

Isso significa dois donos para o mesmo objeto, em repositórios distintos, sem
coordenação automática. Os perigos concretos:

- **Corrida de schema:** o EF do portal pode ter um snapshot que desconhece as colunas
  que o `api-go` adiciona (`mfa_enabled`, `preferences`, `quota_bytes`, etc.). Uma
  migração do .NET que recrie/normalize a tabela pode dropar essas colunas.
- **Tipos divergentes:** `users.id` é `INTEGER` no api-go (FKs em `recovery_codes`,
  `api_keys`, `boards`...). Qualquer mudança de tipo da PK no portal quebra as FKs dos
  serviços Go.
- **`ALTER` destrutivo silencioso:** como cada serviço migra no boot, um deploy
  inocente pode aplicar um `ALTER` que o outro dono não esperava — e só se descobre no
  próximo boot do outro serviço.

## Recomendação

1. **`api-go` é o dono canônico das tabelas de usuário/sessão** (`users`, `sessions`,
   `custom_roles`, `oauth_accounts`). Toda evolução de colunas/índices dessas tabelas
   passa pelo `migrate()` do `api-go`. O portal .NET deve tratá-las como **somente
   leitura de schema** (não recriar, não dropar colunas, não mudar tipo da PK).
2. **Documente antes de qualquer `ALTER`.** Mudança em tabela compartilhada exige:
   atualizar este arquivo, alinhar com o dono do outro repo, e só então aplicar.
   Nunca rode `ALTER`/`DROP` manual em produção sem registrar aqui.
3. **Prefixe tabelas novas** pelo serviço (`pay_`, `claude_`, etc.) para nunca colidir
   com o portal nem com outro serviço.
4. **Migração idempotente sempre.** `CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT
   EXISTS`, `DROP CONSTRAINT IF EXISTS` antes de recriar — o boot pode rodar N vezes.
5. **Sem destrutivo automático.** `DROP TABLE`/`DROP COLUMN`/`RENAME` nunca devem entrar
   num `migrate()` de boot; faça à mão, com backup (ver `docs/backup-restore.md`),
   janela combinada e este documento atualizado.
