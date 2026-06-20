# PIX Automático — Fase 2: checkout público recorrente (Jornada 3)

> Continuação da fase 1 (`2026-06-20-pix-automatico-design.md`, commit 8d0c3ef). A fase 1
> entregou o motor de recorrência + fluxo admin/mensalidade (Jornada 2). Esta fase entrega
> o **checkout público recorrente** (Jornada 3: autoriza + paga a 1ª parcela no mesmo QR).

## Decisões de escopo (confirmadas com o usuário)
- **Admin marca produto como recorrente via UI no dashboard** (incluído nesta fase).
- **Sem self-service do cliente** (sem tela "minhas assinaturas"): o pagador cancela no app
  do banco (direito BACEN) e o admin cancela via `POST /recurrences/{id}/cancel` (fase 1).

## Ressalva Efí (igual à fase 1)
A Jornada 3 (rec + 1ª cobr no mesmo QR de autorização) tem payload mais complexo que a
Jornada 2 e **não foi validada em homolog**. Onde o payload exato for incerto, marcar
`// TODO: validar payload contra Efí em homolog` (padrão do efi.go) e manter o método
isolado e testável. Consultar a doc via WebFetch:
`https://dev.efipay.com.br/docs/api-pix/pix-automatico` (e a página de jornadas).

## 1. Modelo de dados — produtos recorrentes
`pay_products` ganha (migração idempotente em `db.go` + `db/schema.sql`):
```sql
ALTER TABLE pay_products ADD COLUMN IF NOT EXISTS recurring   BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE pay_products ADD COLUMN IF NOT EXISTS periodicity TEXT;     -- MENSAL/etc (null se não recorrente)
ALTER TABLE pay_products ADD COLUMN IF NOT EXISTS due_day     INT;      -- 1-28, dia do débito nos ciclos
```
- `Product` (models.go) ganha `Recurring bool`, `Periodicity string`, `DueDay *int`.
- Queries de produto (sqlc) atualizadas p/ ler/gravar os campos novos (create/update/list/by-slug).
- Validação: se `recurring`, `periodicity` ∈ conjunto válido e `due_day` 1–28 obrigatórios.

## 2. Backend — checkout recorrente (`payments-go`)
Produto recorrente NÃO entra no carrinho multi-item (uma assinatura é item único). Fluxo
dedicado:
- `POST /me/subscribe` (sessão, como `/me/cart/checkout`): body `{ productId, taxId, phone,
  name, email, save }`. Valida que o produto existe e `recurring=true`. UpsertCustomer por
  `(user_id, tax_id)` (igual ao checkout atual). Chama a Efí (Jornada 3) criando a
  recorrência + 1ª cobr (vencimento hoje). Persiste:
  - `pay_recurrences` (status `pending_auth`, `product_id`, `customer_id`, `journey=3`,
    `amount_cents`=preço, `periodicity`, `due_day`).
  - `pay_charges` da 1ª parcela (`kind='recorrente'`, `recurrence_id`, `public_token`,
    `correlation_id`=txid da cobr) — reusa o fluxo de token/SSE/comprovante já existente.
  - Resposta `{ token, brCode, qrCode, amountCents }` (mesma forma do checkout atual) →
    pay-web redireciona pra `/pay/{token}`.
- `efi.go`: `CreateRecurrenceJornada3(ctx, req) (RecurrenceResult + ChargeResult, error)`
  (ou estender `CreateRecurrence` com a jornada). Devolve o QR único de autorização+pagamento
  e o txid da 1ª cobr. TODO-validate.
- Webhooks (já existem, fase 1): `webhookrec` move a recorrência `pending_auth → active` ao
  autorizar; o webhook pix existente marca a 1ª `pay_charges` como `paid` (por `correlation_id`).
- O loop diário da fase 1 (`runRecurrenceCycleLoop`) já cobre os ciclos seguintes (recorrência
  `active`, `due_day`). Garantir que ele respeita o `due_day`/`periodicity` da recorrência
  vinda do produto (não só de mensalidade) — a recorrência já carrega esses campos.

## 3. Frontend — pay-web
- `lib/api.ts`: `Product` ganha `recurring`, `periodicity`, `dueDay`; novo
  `subscribe(productId, payer...)` → `POST /me/subscribe`.
- Produto recorrente (`/p/:slug` e/ou `/:slug`): em vez do fluxo de carrinho, mostra
  "Assinar" e um resumo "R$ X/mês" (formatar periodicidade). O checkout coleta os mesmos
  dados pessoais (`PersonalDataForm`) e chama `subscribe`, depois vai pra `/pay/{token}`.
- `PixView`/CheckoutPage: rótulos de assinatura quando a cobrança é recorrente — "Escaneie
  para autorizar e pagar a 1ª parcela"; no estado pago, "Assinatura ativa" + comprovante.
  Reusar SSE/cancel/receipt existentes (a 1ª cobr é uma pay_charges normal).
- Não criar tela de gerenciamento (self-service deferido).

## 4. Dashboard — form de produto (repo separado `org/dashboard`)
- No formulário de criar/editar produto: toggle "Produto recorrente (assinatura)" que revela
  campos Periodicidade (select) e Dia de vencimento (1–28). Enviar `recurring`, `periodicity`,
  `dueDay` no payload de produto.
- Lista de produtos: badge "Recorrente" + a periodicidade.
- Seguir a identidade visual admin (Geist/Linear/Vercel — ver memória de identidade visual)
  e os padrões de form/query já usados no dashboard.

## 5. Testes
- payments-go: `httptest` + fake Efí para `POST /me/subscribe` (validação 400, produto não
  recorrente 400/409, caminho feliz 201 cria rec+charge), e validação dos campos de produto
  recorrente. Mapeamento/parse Efí já coberto na fase 1.
- pay-web/dashboard: garantir build/lint limpos (sem teste unitário novo obrigatório, mas não
  quebrar os existentes).

## 6. Docs
- `docs/openapi-payments.yaml`: `POST /me/subscribe`, campos novos de `Product`.
- `apps/api-go/llms.txt`: documentar o checkout recorrente e os campos de produto.

## Gate (antes de qualquer commit)
- payments-go: `bash -c "cd apps/payments-go && PATH=\$PATH:\$HOME/.local/bin gofmt -l . && go vet ./... && go build ./... && go test ./..."`.
- pay-web: `cd apps/pay-web && bun run build` (type-check + build); `bun run lint` se existir.
- dashboard (repo `org/dashboard`): `cd web && bun run build` (+ lint).
