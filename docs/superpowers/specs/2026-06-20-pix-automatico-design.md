# PIX Automático (Recorrência Pix BACEN) — payments-go — Design

> **Fase 1 (este spec):** motor de recorrência + fluxo admin/mensalidade (Jornada 2).
> **Fase 2 (depois):** checkout público recorrente em pay-web (Jornada 3).

## Contexto

`apps/payments-go` é o serviço Go de pagamentos PIX via Efí. Hoje ele só usa cobranças
**imediatas** (`/v2/cob/{txid}`). PIX Automático é o padrão BACEN de débito recorrente:
o pagador **autoriza uma vez** e os ciclos seguintes são debitados automaticamente. Usa
endpoints distintos: `/v2/rec` (contrato de recorrência) e `/v2/cobr/{txid}` (cobrança de
cada ciclo). Os scopes `rec`/`cobr`/`solicrec` JÁ estão habilitados na aplicação Efí
(homolog + produção) — confirmado pelo usuário.

Leia `apps/payments-go/efi.go` (especialmente `CreateCharge`, `do`, `doRaw`,
`accessToken`, `efiStatusToApp`, `ParseWebhook`, `RegisterWebhook`) e siga EXATAMENTE
os mesmos padrões (mTLS, token cache, tratamento de `ProviderError`). Leia o CLAUDE.md
raiz: padrões obrigatórios Go (sqlc, rate-limit prefixado, graceful shutdown, logging).

## Jornadas (Efí)

- **Jornada 2 (fase 1):** QR/copia-e-cola que **só autoriza** a recorrência. Nenhum
  débito na hora; o 1º débito cai no `due_day` do próximo ciclo. É o que a mensalidade usa.
- **Jornada 3 (fase 2):** QR único que autoriza E paga a 1ª parcela na hora (checkout).

Referência da API (o agente DEVE consultar via WebFetch ao montar os payloads, pois os
campos exatos não estão 100% nestes docs):
- `https://dev.efipay.com.br/docs/api-pix/pix-automatico` (endpoints, payloads de rec/cobr)
- `POST /v2/rec` (scope `rec.write`): cria recorrência. Campos: `vinculo` (contrato,
  devedor cpf/nome, objeto), `calendario` (dataInicial, dataFinal opcional,
  periodicidade), `valor` (valor fixo), `politicaRetentativa`, location p/ QR.
  Status: CRIADA → APROVADA/REJEITADA/EXPIRADA/CANCELADA.
- `GET /v2/rec/{idRec}` (rec.read), `PATCH /v2/rec/{idRec}` (rec.write, p/ cancelar).
- `PUT /v2/cobr/{txid}` (cobr.write): cria cobrança de um ciclo, vinculada via `idRec`.
- `GET /v2/cobr/{txid}` (cobr.read), `POST /v2/cobr/{txid}/retentativa/{data}` (retry).
- Webhooks separados: `webhookrec` (mudança de status da recorrência) e `webhookcobr` /
  pix recebido (débito do ciclo confirmado). Confirmar formato exato via doc/smoke test;
  onde incerto, marcar `// TODO: validar payload contra Efí em homolog` (padrão já usado
  no efi.go) e manter o parse isolado e testável.

## 1. Modelo de dados

Nova tabela `pay_recurrences` (o contrato) + colunas novas em `pay_charges` (os ciclos).

```sql
CREATE TABLE pay_recurrences (
  id              BIGSERIAL PRIMARY KEY,
  subscription_id BIGINT REFERENCES pay_subscriptions(id) ON DELETE SET NULL,
  product_id      BIGINT REFERENCES pay_products(id),
  customer_id     BIGINT REFERENCES pay_customers(id),
  payer_tax_id    TEXT NOT NULL,
  payer_name      TEXT NOT NULL,
  amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
  periodicity     TEXT NOT NULL DEFAULT 'MENSAL'
                    CHECK (periodicity IN ('SEMANAL','MENSAL','TRIMESTRAL','SEMESTRAL','ANUAL')),
  due_day         INT,
  start_date      DATE NOT NULL,
  end_date        DATE,
  journey         SMALLINT NOT NULL DEFAULT 2,
  efi_id_rec      TEXT,
  br_code         TEXT,
  qr_code         TEXT,
  status          TEXT NOT NULL DEFAULT 'pending_auth'
                    CHECK (status IN ('pending_auth','active','rejected','expired','canceled')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_pay_recurrences_status ON pay_recurrences(status);

ALTER TABLE pay_charges ADD COLUMN recurrence_id BIGINT REFERENCES pay_recurrences(id) ON DELETE SET NULL;
-- kind ganha 'recorrente'; CHECK passa a incluí-lo. provider_charge_id guarda o txid da cobr.
```

Regras:
- `subscription_id` XOR `product_id`: fase 1 só usa `subscription_id` (mensalidade).
- valor FIXO no MVP (`amount_cents`). Valor variável é YAGNI agora.
- A migração vai em `db.go` (idempotente, padrão `ALTER TABLE ... IF NOT EXISTS` /
  `CREATE TABLE IF NOT EXISTS`), e o schema completo em `db/schema.sql` (fonte do sqlc).
- Atualizar o `CHECK (kind IN (...))` de `pay_charges` p/ incluir `'recorrente'`.

## 2. Acesso a dados (sqlc — OBRIGATÓRIO)

Nada de SQL inline. Criar `db/query/recurrences.sql` com queries anotadas:
- `CreateRecurrence :one` — insere e retorna a linha.
- `GetRecurrence :one`, `GetRecurrenceByEfiID :one` (lookup pelo `efi_id_rec` no webhook).
- `ListRecurrences :many` (admin), `SetRecurrenceStatus :exec`,
  `UpdateRecurrenceAuth :exec` (grava `efi_id_rec`, `br_code`, `qr_code` após criar na Efí).
- `RecurrencesDueToday :many` — recorrências `active` cujo ciclo vence hoje (por `due_day`
  e periodicidade) e que ainda não têm cobrança do ciclo (anti-duplicidade, espelhar a
  lógica de `SubscriptionsDueToday`/`uq_pay_charges_sub_month`).
Adicionar em `db/query/charges.sql` o que faltar p/ criar a cobrança de ciclo com
`recurrence_id` e `kind='recorrente'`.
Rodar `/home/guilherme/.local/bin/sqlc generate` na raiz do serviço.

## 3. Camada Efí (`efi.go`)

Novos métodos no `efiProvider`, espelhando o estilo de `CreateCharge`:
- `CreateRecurrence(ctx, RecurrenceRequest) (RecurrenceResult, error)` — `POST /v2/rec`
  (+ location/QR conforme jornada). Retorna `idRec`, `pixCopiaECola`, `qrCode`, status.
- `GetRecurrence(ctx, idRec) (status string, err error)` — `GET /v2/rec/{idRec}`.
- `CancelRecurrence(ctx, idRec) error` — `PATCH /v2/rec/{idRec}`.
- `CreateRecurringCharge(ctx, RecurringChargeRequest) (ChargeResult, error)` —
  `PUT /v2/cobr/{txid}` com `idRec`.
- `ParseRecWebhook(headers, body) ([]RecEvent, error)` — eventos de status da recorrência.
- `RegisterRecWebhook(ctx, url) error` — espelha `RegisterWebhook` (header skip-mtls).
- Mapear status Efí da recorrência → vocabulário do app
  (`pending_auth`/`active`/`rejected`/`expired`/`canceled`), análogo a `efiStatusToApp`.
Os ciclos (`cobr`) usam o MESMO fluxo de status/SSE/webhook de pagamento já existente
(uma cobr paga vira `paid` em `pay_charges`). Atualizar a interface `efiOps` em
`handlers_efi.go` e o `fakeEfiOps` em `handlers_efi_test.go` com os novos métodos.

## 4. Handlers, rotas e loop

- `handlers_recurrence.go` (admin, atrás de `requireAdmin`):
  - `POST /recurrences` — cria recorrência de mensalidade: valida
    `subscriptionId`/`amountCents`/`payerTaxId`/`payerName`/`dueDay`, chama
    `efi.CreateRecurrence` (jornada 2), persiste, devolve `{ id, status, brCode, qrCode }`.
  - `GET /recurrences` — lista (admin).
  - `GET /recurrences/{id}` — detalhe + ciclos (cobranças vinculadas).
  - `POST /recurrences/{id}/cancel` — `efi.CancelRecurrence` best-effort + status local.
- Webhook: rota nova `POST /webhook/rec/{secret}` (ou sufixo análogo ao pix atual) que
  chama `ParseRecWebhook` → `SetRecurrenceStatus`. Reusar a autenticação por segredo na
  URL já usada em `handlers_webhook.go`. O débito de ciclo continua chegando no webhook
  pix existente (txid = cobr) — garantir que o handler atual resolve a cobrança por txid.
- `recurring.go`: novo `runRecurrenceCycleLoop` (boot + ticker diário, padrão de
  `runRecurringLoop`) que busca `RecurrencesDueToday` e cria a `cobr` do ciclo via Efí,
  gravando a `pay_charges` (`kind='recorrente'`, `recurrence_id`). Idempotente. Plugar em
  `main.go` junto aos outros loops (com o mesmo `loopCtx`/graceful shutdown).
- Rate-limit das rotas novas: chaves prefixadas `payments-go:rl:...` (padrão do repo).
- Registrar o webhook de recorrência no boot, ao lado do `RegisterWebhook` atual.

## 5. Models (`models.go`)

`Recurrence` struct (campos da tabela, JSON camelCase como os outros) + structs de
request/result da Efí (`RecurrenceRequest`, `RecurrenceResult`, `RecurringChargeRequest`).

## 6. Testes

- `httptest` + `fakeEfiOps` para os handlers admin (criar/listar/cancelar): casos de
  validação (400) e caminho feliz (201). Sem dependência de Postgres real nos unitários.
- Teste do mapeamento de status da recorrência (tabela de casos, como `efi_test.go`).
- Teste do parse do webhook de recorrência (payload de exemplo → eventos).
- NÃO escrever testes que dependam de autorização real na Efí (isso é smoke manual).

## 7. Docs

- `docs/openapi.yaml`: adicionar as rotas novas (`/recurrences*`, webhook rec).
- `apps/api-go/llms.txt`: documentar os endpoints novos de PIX Automático do payments-go.

## Fora de escopo (fase 2+)

Checkout público recorrente (pay-web, Jornada 3), valor variável, produtos recorrentes
(`pay_products.recurring`), tela "minhas assinaturas" do cliente, retentativa automática
avançada. A coluna `product_id`/`customer_id` já fica na tabela pra essa evolução.

## Gate (antes de qualquer commit)

`cd apps/payments-go` e, com `PATH=$PATH:$HOME/.local/bin`:
`gofmt -l .` (vazio) · `go vet ./...` · `go build ./...` · `go test ./...`.
sqlc em `/home/guilherme/.local/bin/sqlc`.
