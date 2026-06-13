# Bot — Agendamento de aulas via Notion (com confirmação do admin)

**Data:** 2026-06-13 · **Projeto:** `apps/bot-go`

## Objetivo

Permitir que o cliente agende pelo WhatsApp uma **aula experimental** ou uma
**aula individual de adulto**. O bot **lê** a agenda no Notion para checar
disponibilidade, **propõe** um horário ao cliente, o **admin confirma**, e então
o bot **grava** a aula na base "Agenda de Aulas" do Notion e avisa o cliente.

## Decisões (alinhadas com o usuário)

- Tipos agendáveis: **aula experimental** e **aula individual de adulto** (NÃO vaga em turma de grupo, por ora).
- Autonomia: o bot **checa + propõe + pede confirmação ao admin** (reusa o fluxo de confirmação já existente). Não marca sozinho.
- Após o "confirma" do admin, **o bot grava** a aula no Notion (escrita gated por humano).
- Acesso ao Notion é feito por **código Go determinístico** com **token escopado** — o LLM nunca toca no Notion (segurança: cliente é não-confiável).

## Pré-requisito (runtime)

Token de integração interna do Notion, compartilhado só com a base "Agenda de Aulas":
- `NOTION_TOKEN` (env, `secret_...`)
- `NOTION_AGENDA_DB_ID` (env, default `1e1c30d6-77df-44dd-8f2f-6889777de5cc`)

Sem o token, o bot **degrada com graça**: não injeta disponibilidade e não grava
(o agendamento vira só uma notificação ao admin). Todo o código é escrito sem o token.

## Arquitetura

### Cliente Notion (`notion.go`)
- `NotionClient{ token, dbID, http }`.
- `Schedule(ctx) ([]ScheduleEntry, error)` — `POST /v1/databases/{dbID}/query`; parseia Dia, Horário, Período, Professor, Conteúdo, Aluno. Cache TTL ~5min (como o sitemap).
- `CreateBooking(ctx, Booking) error` — `POST /v1/pages` com parent `database_id`; propriedades: `Aula` (title), `Dia` (select), `Horário` (rich_text), `Período` (select), `Data` (date), `Conteúdo` (select, opcional), `Professor` (select, opcional).
- Header `Notion-Version: 2022-06-28`. Erros não-200 → erro; chamadas best-effort no fluxo.

Propriedades da base (fonte: fetch): `Aula`(title), `Dia`(select Seg–Sáb), `Horário`(text), `Período`(select Manhã/Tarde/Noite), `Professor`(select Amanda/Samuel/Rodrigo), `Aluno`(multi_select — opções fixas, NÃO usar para lead novo), `Conteúdo`(select), `Duração`(multi_select), `Data`(date).

### Disponibilidade no prompt do cliente
- O Responder (AgentGoClient) busca `Schedule()` (cacheado) e injeta em `cfg` (transient `ScheduleInfo`).
- `BuildPrompt` (cliente) ganha uma seção **"Agenda e agendamento"**: horário de funcionamento (Seg–Sex 8–22h, Sáb 8–18h), os horários **já ocupados** (compacto, por dia), e instruções: como agendar (experimental/individual), o que coletar (nome; idade se criança; curso de interesse; dias/horários que prefere) e que deve **propor um horário livre** e preencher `schedulingRequest`.
- Se o Notion não estiver configurado/falhar, a seção é omitida (degrada).

### Saída estruturada do cliente (novo campo)
```json
"schedulingRequest": {
  "kind": "experimental" | "individual",
  "studentName": "...", "age": 0, "course": "...",
  "proposedDay": "Terça", "proposedTime": "19h30", "proposedPeriod": "Noite",
  "notes": "..."
}
```
Preenchido SOMENTE quando o cliente quer agendar e há info suficiente + um horário proposto. O balão ao cliente confirma a proposta ("posso marcar terça 19h30?").

### Pendência de agendamento + confirmação do admin
- Nova tabela `pending_booking` (espelha `pending_question`): `{id, tenant_id, conversation_id, client_phone, client_name, kind, course, proposed_day, proposed_time, proposed_period, age, notes, status(open|confirmed|rejected), created_at}`.
- Ao receber `schedulingRequest`, o engine grava `pending_booking` (status `open`) e notifica o admin (reusa `notificaAdmin`/evento).
- O modo admin passa a injetar as **"Agendamentos aguardando confirmação"** (como já faz com dúvidas pendentes), e ganha `bookingActions`: `[{bookingId, action: "confirm"|"adjust"|"reject", day, time, period}]`.
  - `confirm` → engine: `NotionClient.CreateBooking(...)` + envia ao cliente "marcado!" (reusa o envio cross-conversa de `executeClientActions`) + `pending_booking.status='confirmed'`.
  - `adjust` → atualiza a proposta e re-mostra ao admin (ou já confirma com o novo horário).
  - `reject` → status `rejected` + (opcional) avisa o cliente.

### Reuso
- A notificação ao admin, a injeção de pendências no prompt admin e o envio cross-conversa ao cliente **já existem** (fluxo de dúvidas pendentes). Agendamento segue o mesmo padrão, com uma tabela e um campo de ação próprios.

## Arquivos
- `notion.go` (novo) — cliente Notion (read/write) + cache.
- `config.go` — `NotionToken`, `NotionAgendaDBID`.
- `main.go` — instancia `NotionClient`, injeta no Responder e no engine.
- `types.go` — `ScheduleEntry`, `Booking`, `SchedulingRequest`, `BookingAction`, campos em `ResponderOutput`, `ConversationContext`, `TenantConfig` (transient `ScheduleInfo`).
- `prompt.go` — seção de agenda no prompt do cliente; seção de agendamentos + `bookingActions` no prompt do admin.
- `parser.go` — parsear `schedulingRequest` e `bookingActions`.
- `repos.go` — `PendingBookingRepo` (Insert, ListOpen, Get, MarkStatus).
- `migrations/0018_pending_booking.sql` — tabela.
- `agent_go.go` — injeta `ScheduleInfo` no cfg antes do `BuildPrompt`.
- `engine.go` — grava pendência no `schedulingRequest`; injeta pendências no modo admin; executa `bookingActions` (grava no Notion + responde cliente).

## Não-objetivos (YAGNI)
- Vaga em turma de grupo.
- Cálculo perfeito de disponibilidade por professor/sala (o admin é a autoridade final).
- LLM com acesso direto ao Notion (MCP).

## Plano de implementação
1. `notion.go` (cliente + cache) + config + types base. *(sem token = compila, runtime degrada)*
2. Migration `0018` + `PendingBookingRepo` + wiring `main.go`.
3. Injeção de disponibilidade no prompt do cliente (`agent_go.go` + `prompt.go`).
4. `schedulingRequest` no parser + gravação da pendência + notificação admin (`engine.go`).
5. Prompt admin com agendamentos + `bookingActions` (`prompt.go` + `parser.go`).
6. Execução do `bookingActions` no engine (CreateBooking + responde cliente).
7. `gofmt`/`vet`/`build`; deploy; pedir o `NOTION_TOKEN` ao usuário p/ validar e2e.
