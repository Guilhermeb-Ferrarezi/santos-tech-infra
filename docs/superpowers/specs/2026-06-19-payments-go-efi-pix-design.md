# payments-go — Provider Efí Pix (substituindo o Dotfy)

**Data:** 2026-06-19
**Serviço:** `apps/payments-go`
**Objetivo:** trocar o gateway PIX do `payments-go` do Dotfy para a **Efí** (ex-Gerencianet),
usando a API Pix da Efí com mTLS. Começar em **homologação**; produção depois.

## Contexto

O `payments-go` já isola o gateway atrás da interface `PaymentProvider`
(`provider.go`): `CreateCharge`, `GetCharge`, `ParseWebhook`. Hoje a única
implementação é `dotfyProvider`, fixada em `main.go` (`provider := newDotfyProvider(cfg)`).
Todo o resto — `store`, `hub`, SSE, `jobs`, `recurring`, cobrança recorrente,
modelos — depende **só** da interface, não do gateway concreto.

O fluxo é **100% webhook-driven**: quando a cobrança é paga, `handleDotfyWebhook`
(`handlers_webhook.go`) faz `ParseWebhook → MarkWebhookSeen (idempotência) →
switch no Type → MarkChargePaid/Expired`, invalida cache, acorda o SSE e enfileira
a notificação. `provider.GetCharge` **não** é usado no fluxo (não há polling).

## Decisões

| Tema | Decisão |
|------|---------|
| Método de cobrança | **PIX** (API Pix da Efí) |
| Relação com o Dotfy | **Substituição total** — Dotfy removido do repo |
| Confirmação de pagamento | **Webhook-only** (mantém a arquitetura atual; sem polling) |
| Entrega do certificado | **Base64 em env var** (`EFI_CERT_P12_BASE64`), decodificado no boot |
| Ambiente inicial | **Homologação** (`https://pix-h.api.efipay.com.br`) |

### Limitação aceita
A API Pix da Efí, via webhook, **só notifica pagamento** (array `pix[]`) — **não
notifica expiração**. Com webhook-only, cobrança vencida não vira `expired` no
banco; fica `pending`. O `ExpiresAt` já existe na cobrança, então a exibição/consulta
trata vencida como expirada. Não é simétrico, mas é aceitável para começar.

## Arquitetura

A Efí entra como a **única** implementação de `PaymentProvider`.

- **Novo `efi.go`** (`package main`): `efiProvider`, `newEfiProvider(cfg)` e os 3 métodos.
- **Dotfy removido:** apagar `dotfy.go`, `dotfy_test.go`, env `DOTFY_*` (`config.go`),
  e trocar `newDotfyProvider` → `newEfiProvider` no `main.go`.
- **Webhook:** `handleDotfyWebhook` → `handleWebhook`; rota nova `POST /webhook/efi/pix`.
- **Intocado:** `store`, `hub`, SSE, `jobs`, `recurring`, modelos.
- `ChargeResult` (BRCode/QRCode/PaymentLink/Status) já é PIX-puro → encaixa direto.

## Autenticação & mTLS

A API Pix exige **mTLS**: o certificado de cliente entra no handshake TLS de **toda**
chamada.

1. `POST /oauth/token` — Basic auth `base64(client_id:client_secret)` + body
   `{"grant_type":"client_credentials"}`, **sobre mTLS**. Retorna `access_token` (~1h).
2. Token **cacheado em memória** (mutex + expiry, renova ~60s antes de vencer). Cada
   réplica cacheia o seu; sem Redis.
3. Todas as chamadas `/v2/...`: `Authorization: Bearer <token>` + o mesmo cert no transport.

**Go:** um único `*http.Client` com `Transport.TLSClientConfig.Certificates = [cert]`,
reusado para token e cobranças. O `.p12` é parseado com
`software.sslmate.com/src/go-pkcs12` → `tls.Certificate`. O `.p12` da Efí homolog abre
com **senha vazia** (validado: `CN=930026`, emissor `apis-h.efipay.com.br`).

**Cert no deploy:** `EFI_CERT_P12_BASE64` (secret no Coolify) decodificado no boot.
Sem volume, sem arquivo no disco. Homolog e prod têm certificados distintos.

## CreateCharge / GetCharge

**CreateCharge** — usa **nosso** txid: `PUT /v2/cob/{txid}`
```json
{
  "calendario": {"expiracao": <segundos até ExpiresAt>},
  "valor": {"original": "<reais, 2 casas>"},
  "chave": "<EFI_PIX_KEY>",
  "solicitacaoPagador": "<Description>",
  "devedor": {"nome": "<PayerName>", "cpf": "<PayerTaxID>"}
}
```
- `devedor` só quando `PayerTaxID` vier (cpf/cnpj só dígitos).
- Resposta: `pixCopiaECola` → **BRCode**; `txid` → `ProviderChargeID`; `status: ATIVA` → `pending`.
- **QR imagem:** 1 GET extra `/v2/loc/{loc.id}/qrcode` → `imagemQrcode` (data-uri PNG) → `QRCode`.
  Sem lib de QR no Go; é o QR oficial da Efí.
- Usa `/v2/cob` (cobrança imediata com `expiracao`), que casa com o modelo de
  expiração-por-timestamp do app. `/v2/cobv` (vencimento com multa/juros) fica para depois (YAGNI).
- Erro do gateway: Efí devolve `{nome, mensagem, violacoes[]}` → embrulhar em
  `*ProviderError` com a mensagem amigável (padrão atual que repassa msg ao cliente).

**GetCharge** — `GET /v2/cob/{txid}`; mapeia `ATIVA→pending`, `CONCLUIDA→paid`,
`REMOVIDA_*→expired`. Implementado para fechar a interface (admin/futuro), mesmo fora
do fluxo webhook-only.

### Ajustes obrigatórios no `handlers_charges.go`
- `newCorrelationID()` devolve hoje `stpay_<32hex>` = **38 chars com `_`**. A Efí
  **rejeita**: txid é **26–35 caracteres alfanuméricos** (sem `_`). Trocar para
  `stpay<28hex>` = **33 chars alnum**.
- `Provider: "dotfy"` (linha ~64) → `"efi"`.
- O override `if res.CorrelationID != ""` deixa de disparar: como **nós** definimos o
  txid, `efiProvider.CreateCharge` devolve `CorrelationID:""` e mantemos o nosso — que
  é exatamente o que volta no webhook.

## Webhook

1. **Registro:** `PUT /v2/webhook/{chave}` body `{"webhookUrl":"https://pay.../webhook/efi"}`,
   com header **`x-skip-mtls-checking: true`** (atrás de Cloudflare + Traefik o mTLS de
   volta da Efí quebra). A Efí **anexa `/pix`** à URL → chama `.../webhook/efi/pix`.
2. **Segurança sem mTLS:** segredo na URL que só a Efí conhece (nós registramos):
   `.../webhook/efi?hmac=<EFI_WEBHOOK_SECRET>`. A Efí ecoa a query nos callbacks; o
   handler compara em **tempo constante**. Fail-closed em produção: sem
   `EFI_WEBHOOK_SECRET`, recusa (igual ao guard atual).
3. **Payload:** `{"pix":[{endToEndId, txid, valor, horario, ...}]}`. **Sem "tipo":**
   receber um `pix` casado com nosso `txid` = **pago**. Cada item →
   `WebhookEvent{Type:"CHARGE_PAID", ID:endToEndId (idempotência), CorrelationID:txid}`.
4. **Mudança de interface:** `ParseWebhook` passa a devolver **`[]WebhookEvent`** (a Efí
   pode mandar vários `pix` por POST). O handler vira loop; `MarkWebhookSeen(ev.ID)` por
   item dá idempotência por `endToEndId`. A validação do segredo sobe para o handler
   (ele tem a URL/query); `ParseWebhook` só mapeia payload.
5. **Ping de teste:** ao registrar, a Efí dispara notificação de teste (sem `pix[]`) →
   `ParseWebhook` devolve lista vazia (não erro) → handler responde 200.
6. **Registro one-off:** subcomando `payments-go -register-webhook`, rodado **uma vez por
   ambiente** (usa o mTLS já montado no serviço). Não re-registra a cada deploy nem trava
   o boot.

## Configuração (env)

Novas (`config.go` + `.env.example`):

| Var | Obrigatória | Default | Descrição |
|-----|-------------|---------|-----------|
| `EFI_BASE_URL` | não | `https://pix-h.api.efipay.com.br` (homolog) | prod: `https://pix.api.efipay.com.br` |
| `EFI_CLIENT_ID` | sim | — | client_id do app Efí |
| `EFI_CLIENT_SECRET` | sim | — | client_secret do app Efí |
| `EFI_CERT_P12_BASE64` | sim | — | base64 do `.p12` (`base64 -w0 cert.p12`) |
| `EFI_CERT_PASSWORD` | não | `` (vazio) | senha do `.p12` (Efí normalmente vazio) |
| `EFI_PIX_KEY` | sim | — | chave Pix recebedora |
| `EFI_WEBHOOK_SECRET` | sim em prod | — | segredo validado na URL do webhook |

Removidas: `DOTFY_BASE_URL`, `DOTFY_API_KEY`, `DOTFY_WEBHOOK_SECRET`, `DOTFY_WEBHOOK_SIG_HEADER`.

## Testes

`httptest` mockando a Efí (mTLS fora do teste), seguindo o padrão `_test.go` atual:
- `efiProvider`: cache de token (não rebusca antes de expirar), payload do `CreateCharge`,
  parse de `pixCopiaECola`/`txid`, erro da Efí → `*ProviderError`.
- `ParseWebhook`: um `pix`, vários `pix`, ping de teste (lista vazia, sem erro),
  segredo errado rejeitado.

## Deploy (Coolify)

- App `payments-go` já existe → `watch_paths` **não muda**.
- Setar `EFI_*` como secrets (incl. `EFI_CERT_P12_BASE64`); `EFI_BASE_URL` = homolog.
- Rodar `-register-webhook` uma vez no ambiente.
- Padrões Go obrigatórios já presentes no serviço (graceful shutdown, `/ready`,
  `/metrics`, rate-limit `payments-go:`, sqlc, CI). `efi.go` é só HTTP de provider (não
  toca banco) → nada a regularizar.

## Docs a atualizar (mesmo conjunto de mudanças)

- `docs/openapi-payments.yaml`: rota do webhook → `/webhook/efi/pix`.
- `apps/api-go/llms.txt`: ajustar menção a provider/webhook de pagamentos, se houver.
- `apps/payments-go/.env.example`: `EFI_*` (placeholders), remover `DOTFY_*`.

## Fora de escopo

- Boleto/cartão (API de Cobranças da Efí).
- `/v2/cobv` (cobrança com vencimento, multa/juros).
- Polling/reconciliação de cobranças pendentes.
- Marcar expiração automática no banco.
