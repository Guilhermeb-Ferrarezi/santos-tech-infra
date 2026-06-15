# API de Pagamentos — Pix (Fase 1)

**Data:** 2026-06-15 · **Projeto:** `apps/payments-go` (novo serviço Go no monorepo)

## Objetivo

API de pagamentos da Santos Tech para **cobrar mensalidades e matrículas/avulsos via
Pix**, usando o gateway **Dotfy** (Pix puro). Como o Dotfy **não tem assinatura
recorrente nativa**, a recorrência das mensalidades é responsabilidade **da nossa API**:
um job mensal gera uma cobrança Pix por aluno ativo, envia o QR/copia-e-cola por email,
e o webhook `CHARGE_PAID` dá baixa. Inadimplência = cobrança que expira sem pagamento.

O sistema é **multi-provider por design** (interface `PaymentProvider`); cartão recorrente
via **Stripe** entra na **Fase 2** sem tocar no núcleo.

## Decisões (alinhadas)

- **Escopo Fase 1:** mensalidade recorrente (Pix) + matrícula/avulso (Pix). Cartão fica p/ Fase 2.
- **Gateway:** Dotfy (Pix). Base URL **`https://app.dotfy.com.br`** · adquirente **TREEAL**.
  Endpoint `POST /api/charges`, webhooks `CHARGE_CREATED/PAID/EXPIRED`. **Auth confirmada:**
  `Authorization: Bearer <key>` (também aceita `x-api-key`); key prefixo `vk_live_`/`vk_test_`.
  Conta tem `pixRecurrenceEnabled: false` → recorrência nativa indisponível, confirma a
  recorrência do nosso lado.
- **Alunos:** cadastro próprio neste serviço (independente do `portal-do-aluno`; sync depois).
- **Notificação:** email pelo gateway existente (`POST {EMAIL_API_URL}/send`); API também
  devolve o QR/copia-e-cola no JSON da resposta.
- **Auth:** **sem login próprio** — valida o **mesmo JWT HS256** do ecossistema
  (`apps/api-go`). Painel admin exige papel `Admin` (3). Webhook do Dotfy é rota pública verificada.
- **Recorrência:** goroutine com ticker diário in-process (sem cron externo).
- **App:** `apps/payments-go`, mesmo Postgres 16 da infra, com tabelas próprias.

## Stack (segue o padrão `apps/api-go`)

| Camada | Tecnologia |
|--------|-----------|
| Linguagem | Go 1.25, `package main` flat |
| HTTP | `net/http` stdlib (mux `"POST /rota"`, sem framework) |
| Banco | PostgreSQL 16 via `pgx/v5` (mesmo banco do ecossistema, tabelas próprias) |
| JWT | `golang-jwt/v5` (HS256) — **valida**, não emite |
| Email | cliente HTTP da API de emails (`POST {EMAIL_API_URL}/send`) |
| Logs | `slog` estruturado em stdout |
| Migrations | `migrate()` idempotente no boot (padrão `db.go`) |

## Arquitetura

### Camadas
- **`PaymentProvider`** (interface) — desacopla o núcleo do gateway:
  - `CreateCharge(ctx, ChargeRequest) (ChargeResult, error)` — cria cobrança Pix.
  - `GetCharge(ctx, providerChargeID) (ChargeStatus, error)` — consulta status.
  - `ParseWebhook(headers, body) (WebhookEvent, error)` — valida e normaliza o evento.
  - Impl `dotfyProvider` agora; `stripeProvider` na Fase 2 — núcleo não muda.
- **Núcleo** (independente de gateway): alunos, planos, assinaturas, cobranças, inadimplência.

### Dados (Postgres — migrations próprias, idempotentes no boot)
- `pay_students` — aluno/responsável: `id`, `name`, `tax_id` (CPF), `email`, `phone`, `created_at`.
- `pay_plans` — plano de mensalidade: `id`, `name`, `amount_cents`, `due_day` (1–28), `active`.
- `pay_subscriptions` — vincula aluno↔plano: `id`, `student_id`, `plan_id`, `amount_cents`
  (override opcional do plano), `due_day`, `status` (`active`/`paused`/`canceled`), `created_at`.
- `pay_charges` — cada cobrança:
  - `id`, `kind` (`mensalidade`/`matricula`/`avulso`), `subscription_id` (nullable),
    `student_id`, `amount_cents`, `due_date`, `reference_month` (p/ mensalidade, idempotência),
  - `status` (`pending`/`paid`/`expired`/`canceled`),
  - `provider` (`dotfy`), `provider_charge_id`, `correlation_id` (único),
    `br_code` (copia-e-cola), `qr_code`, `paid_at`, `created_at`.
  - **Única `(subscription_id, reference_month)`** → impede mensalidade duplicada no mês.
- `pay_webhook_events` — idempotência: `id` (event id do provider), `type`, `payload` (jsonb),
  `processed_at`. Insert-if-not-exists antes de processar.

### Fluxos
1. **Matrícula / avulso** — `POST /charges` (admin):
   valida payload → `provider.CreateCharge` → grava `pay_charges` (`pending`) →
   responde `{ id, amount, brCode, qrCode, dueDate }` → dispara email com o Pix.
2. **Mensalidade (recorrência nossa)** — ticker diário:
   varre `pay_subscriptions` ativas cujo `due_day` == hoje (ou janela de antecedência) →
   para cada uma, se ainda não existe charge do `reference_month`, gera via Dotfy →
   grava → envia email. Idempotente pela única `(subscription_id, reference_month)`.
3. **Webhook Dotfy** — `POST /webhooks/dotfy` (público, verificado):
   `provider.ParseWebhook` valida origem/assinatura → grava em `pay_webhook_events`
   (ignora se já processado) → `CHARGE_PAID`: marca charge `paid` + `paid_at`;
   `CHARGE_EXPIRED`: marca `expired`. Sempre responde `200` rápido.
4. **Inadimplência** — status derivado das charges; endpoints de consulta + base p/ lembretes.

### Endpoints (admin, JWT papel Admin; convenção de erro `{code,message}` em PT)
| Método | Rota | Descrição |
|--------|------|-----------|
| GET  | `/health` | liveness |
| POST | `/students` · GET `/students` · GET `/students/{id}` | CRUD aluno |
| POST | `/plans` · GET `/plans` | planos de mensalidade |
| POST | `/subscriptions` · GET `/subscriptions` · PATCH `/subscriptions/{id}` | matrícula em plano (status) |
| POST | `/charges` | cria cobrança avulsa/matrícula (Pix) |
| GET  | `/charges` | lista (filtros: `status`, `student_id`, `kind`) |
| GET  | `/charges/{id}` | detalhe (inclui brCode/qrCode/status) |
| GET  | `/students/{id}/charges` | histórico do aluno (inadimplência) |
| POST | `/webhooks/dotfy` | **público**, verificado — eventos do gateway |

### Segurança
- **API key do Dotfy** em env/secret (`DOTFY_API_KEY`), nunca commitada. Header de auth
  confirmado contra a conta real via `GET /api/auth/me` na implementação.
- **Webhook** verificado (assinatura/secret do Dotfy se houver; senão, validação de
  origem + idempotência por event id). Responde 200 rápido, processa idempotente.
- **JWT** HS256 compartilhado (mesmo `JWT_SECRET`); middleware exige papel Admin nas rotas admin.
- Sem segredos no repo; `.env.example` documentando as variáveis.

### Variáveis de ambiente
| Variável | Descrição |
|----------|-----------|
| `DATABASE_URL` | Postgres do ecossistema (tabelas `pay_*`) |
| `JWT_SECRET` | **igual** ao dos outros serviços (valida access token) |
| `DOTFY_API_KEY` | chave da API Dotfy (`vk_live_*` / `vk_test_*`), header `Authorization: Bearer` |
| `DOTFY_BASE_URL` | base da API Dotfy (default `https://app.dotfy.com.br`) |
| `DOTFY_WEBHOOK_SECRET` | segredo de verificação do webhook (se o Dotfy oferecer) |
| `EMAIL_API_URL` / `EMAIL_API_KEY` | API de emails (envio do Pix) |
| `PORT` | porta do serviço (ex.: 3334) |
| `COOKIE_DOMAIN` / `CORS_ORIGIN` | conforme padrão do ecossistema |

### Testes
- **Unit (núcleo):** geração de mensalidade (idempotência por `reference_month`),
  transições de status (pending→paid/expired), idempotência de webhook, cálculo de vencimento.
  `PaymentProvider` **mockado** — sem rede.
- **Adapter Dotfy:** testado contra sandbox/`GET /api/auth/me`; `httptest` p/ parsing de webhook.

## Não-objetivos (YAGNI / Fase 2+)
- Cartão recorrente (Stripe), boleto.
- Splits, subcontas white-label (BaaS), saques/chaves Pix, disputas/MEDs, dashboard de analytics.
- Integração direta com `portal-do-aluno` (cadastro é próprio nesta fase).
- Front-end/painel visual (a API só expõe JSON; painel pode vir depois).
  - **Planejado (Fase 2):** **tela de checkout Pix própria** da Santos Tech (NÃO a do Dotfy).
    A API já devolve `brCode` (copia-e-cola) + `qrCode` no JSON de cada cobrança, então o
    front próprio consome esses campos e renderiza o Pix com a identidade visual de vocês.

## Plano de implementação (alto nível)
1. Scaffold `apps/payments-go` (`main.go`, `config.go`, `server.go`, `db.go`, `models.go`,
   `errors.go`) no padrão `api-go`; migrations `pay_*`; `/health`.
2. Núcleo + repositórios (students/plans/subscriptions/charges) + endpoints admin com JWT.
3. Interface `PaymentProvider` + `dotfyProvider` (CreateCharge/GetCharge/ParseWebhook),
   confirmando payload/auth contra a doc/conta real.
4. Webhook `/webhooks/dotfy` + idempotência (`pay_webhook_events`).
5. Job de recorrência (ticker diário) + envio de email (cliente `email.go`).
6. Testes unit (núcleo) + adapter; `gofmt`/`go vet`/`go build`/`go test`.
7. `Dockerfile.payments-go` + serviço no `infra/docker-compose.yml`; `docs/openapi.yaml`
   e `llms.txt` central atualizados.
