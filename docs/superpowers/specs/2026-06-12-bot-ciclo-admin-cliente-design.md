# Bot — Ciclo admin → cliente (com confirmação leve)

**Data:** 2026-06-12 · **Projeto:** `apps/bot-go`

## Problema

Hoje, quando o bot não sabe responder um cliente (handoff), ele só avisa o admin.
O admin cadastra a informação no modo admin, mas:

1. O bot é **prolixo/insistente** — pede confirmação várias vezes mesmo após "ta certo".
2. O modo admin e a conversa do cliente são **desconectados**: o bot aprende a info
   mas não volta sozinho para responder o cliente que estava esperando. ("responde o
   cliente la" → "não estou vendo nenhuma mensagem de cliente em aberto").

## Objetivo

- **A.** Enxugar o modo admin: aceitar a info com no máximo UMA pergunta de
  esclarecimento; confirmar em UMA mensagem curta.
- **B+C.** Fechar o ciclo: admin cadastra a info → bot **rascunha** a resposta ao
  cliente → admin dá um "ok" leve → bot envia ao cliente → bot confirma ao admin.

## Decisões (alinhadas com o usuário)

- Disparo **não** é 100% automático: o bot mostra um **rascunho** e só envia ao cliente
  após confirmação leve do admin ("sim" / "pode mandar"), correções reescrevem o rascunho.
- O bot **casa pela pergunta**: identifica qual dúvida pendente a info responde.
- Toda a orquestração (casar, rascunhar, confirmar) acontece **dentro da conversa do
  admin** — o LLM vê o histórico, então "sim"/correções funcionam naturalmente.
- Feedback ao admin: **uma** mensagem por rodada, dizendo o que foi enviado.
- "Avisar se funcionou" = confirmar **o que foi enviado e que foi entregue**. Saber se
  o cliente ficou satisfeito (resposta dele depois) fica para um segundo momento.

## Arquitetura

### Nova tabela `pending_question`
Fila de dúvidas de clientes aguardando resposta.

| coluna | tipo | nota |
|--------|------|------|
| id | text PK | `pq_<unixmilli>` |
| tenant_id | uuid/text | FK lógico do tenant |
| conversation_id | text | conversa do **cliente** |
| client_phone | text | external_id (WhatsApp) |
| client_name | text | nome do contato (pode ser vazio) |
| question | text | a dúvida não respondida |
| status | text | `open` → `drafted` → `resolved` |
| draft | text NULL | rascunho proposto da resposta |
| created_at / updated_at | timestamptz | |

Índice parcial por `(tenant_id, status)` para listar pendências abertas.

### Fluxo

1. **Handoff (cliente sem resposta).** Em `engine.go` Fase 3, quando `newState =
   HANDOFF`, além da notificação existente, `INSERT pending_question` com a pergunta
   (`inboundText`), telefone e nome do cliente, `status='open'`.

2. **Admin manda a info.** Modo admin já roda hoje. O `buildAdminPrompt` passa a:
   - Receber as pendências **abertas/rascunhadas** injetadas (id, nome, telefone,
     pergunta, rascunho atual).
   - Instruir o LLM a, ao receber info do admin: gerar `kbEntry` (salvar no KB) e, se
     casar com alguma pendência, propor `clientActions`.
   - Enxugar (Parte A): no máx. 1 pergunta de esclarecimento, confirmação curta.

3. **Saída estruturada do modo admin** (novo campo `clientActions`):
   ```json
   { "bubbles":["..."], "answered":true, "answeredFromKb":false, "handoff":false,
     "kbEntry": {"title":"...","content":"..."},
     "clientActions": [ {"pendingId":"pq_123","draft":"A mensalidade é R$ 250","send":false} ] }
   ```
   - `send:false` → engine grava `draft`, `status='drafted'`. A bolha do bot ao admin
     já mostra o rascunho e pede confirmação.
   - `send:true` → engine **envia ao cliente** (`Sender.SendMessage` + `RecordOutbound`
     na conversa do cliente), volta a conversa do cliente para `ENGAGED`, marca a
     pendência `resolved`. A bolha do bot ao admin já confirma o envio.

4. **Sem pendência casada** → `clientActions` vazio → só salva no KB (cobre cadastro
   proativo sem cliente esperando).

### Execução das `clientActions` (engine, síncrono)
Nova fase entre o envio das bolhas do admin (Fase 2) e o FSM (Fase 3):
- Para cada ação `send:false`: `UPDATE pending_question SET draft, status='drafted'`.
- Para cada ação `send:true` (idempotente, só se `status != 'resolved'`):
  `withTenant`: `SendMessage` ao `client_phone` → `RecordOutbound` (chave
  `pendingId:reply`) → `ConversationRepo` set state `ENGAGED` → `UPDATE pending
  status='resolved'`. Se o envio falhar, manda um texto extra ao admin avisando.

### Injeção das pendências no prompt
`ConversationContext` ganha `PendingQuestions []PendingQuestion`, populado **só** para
conversas admin (engine busca via novo `PendingQuestionRepo.ListOpen` na Fase 1).
`buildAdminPrompt` itera essa lista.

## Arquivos

- `migrations/0015_pending_question.sql` — tabela + índice.
- `types.go` — `PendingQuestion`, `ClientAction`, campo em `ResponderOutput` e
  `ConversationContext`.
- `repos.go` — `PendingQuestionRepo` (`Insert`, `ListOpen`, `StoreDraft`,
  `MarkResolved`); helper para nome/telefone do cliente por conversa (já vem do handoff).
- `parser.go` — parsear `clientActions` (tolerante, descarta malformado).
- `prompt.go` — injetar pendências + `clientActions` no schema admin + enxugar.
- `engine.go` — inserir pendência no handoff; executar `clientActions`; popular
  `convCtx.PendingQuestions` no modo admin.
- `main.go` — instanciar/injetar `PendingQuestionRepo`.

## Não-objetivos (YAGNI)
- Medir satisfação do cliente / encadear a resposta dele depois.
- Suporte a múltiplos admins simultâneos.
- UI de pendências no dashboard (pode vir depois).

## Plano de implementação
1. Migration `0015` + `PendingQuestion`/`ClientAction` em `types.go`.
2. `PendingQuestionRepo` em `repos.go` + wiring no `main.go`.
3. Inserir pendência no handoff (`engine.go` Fase 3).
4. Injetar pendências no `buildAdminPrompt` + enxugar + schema `clientActions`.
5. Parsear `clientActions` (`parser.go`).
6. Executar `clientActions` no `engine.go` (nova fase), com idempotência e aviso de falha.
7. `gofmt`/`go vet`/`go build`; deploy; teste manual ponta a ponta.
