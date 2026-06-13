# CRM — Funil de leads do WhatsApp (Kanban)

**Data:** 2026-06-13 · **Projetos:** `apps/bot-go` (backend) + `Santos-Techrp/dashboard` (frontend)

## Objetivo

CRM básico: **todo cliente que manda mensagem vira um lead**, exibido num **Kanban**
de 5 estágios. O atendimento continua no **WhatsApp** (o card só tem botão "Abrir no
WhatsApp"; sem chat embutido).

## Decisões (alinhadas)

- Estágios (funil de escola): `novo → em_atendimento → aula_marcada → matriculado → perdido`.
- Visual: **Kanban**, cards arrastáveis entre colunas (drag-and-drop nativo, sem dep nova).
- Captura automática: lead criado no inbound do cliente (idempotente, 1 por contato).
- Atendimento no WhatsApp: card tem botão `wa.me/<telefone>`.
- **Tie-in:** ao confirmar um agendamento, o lead vai para `aula_marcada` automaticamente.
- **Sidebar:** "WhatsApp" vira grupo pai com 2 subgrupos colapsáveis — **IA** (bot:
  Visão geral, Conversas, Base de conhecimento, Configurações, Logs) e **CRM** (Funil de leads).
- **Permissão:** reusa `whats` (sem recurso de cargo novo).

## Arquitetura

### Dados — reusa a tabela `lead` (0001)
Colunas: `status` (free text → padronizo no funil), `interest`, `owner`, `email`. Um por contato.
- Migration `0019`: normaliza status legados (`new`/`open`/`''`) → `novo`.
- `LeadRepo.Create` passa a inserir status `novo`. Chamado no inbound do cliente (engine Fase 1, idempotente, só não-admin).

### Backend (bot-go) — endpoints dashboard
- `GET /api/leads` → lista: `{id, contactName, phone, status, interest, owner, lastActivity, conversationId, createdAt}` (join lead→contact→channel_identity→conversation).
- `PATCH /api/leads/{id}` → atualiza `status` (e opcional `owner`/`interest`). Valida status ∈ funil.
- `LeadRepo`: `List(tenant)`, `UpdateStatus(id, status)`, `SetStatusByConversation(convID, status)` (para o tie-in).
- Tie-in: em `executeBookingActions` (confirm), chama `SetStatusByConversation(pb.ConversationID, "aula_marcada")` — só avança se o lead não estiver já em matriculado/perdido.

### Frontend (dashboard)
- `web/src/lib/whats.ts`: `BotLead` + `bot.leads()` + `bot.patchLead(id, {status})`.
- Página nova `web/src/pages/admin/WhatsCRM.tsx`: Kanban 5 colunas; cards com nome, telefone, interesse, última atividade e botão "Abrir no WhatsApp" (`wa.me`). Drag nativo (HTML5: draggable + onDragStart/onDrop) → `PATCH` status (otimista, com invalidate).
- Rota `/admin/crm` em `web/src/App.tsx` (Protected, permissão `whats`).
- **Sidebar/nav** (`web/src/lib/nav.ts` + componente da sidebar): suportar **subgrupos**. O grupo "WhatsApp" ganha `subgroups: [{label:"IA", items:[...bot]}, {label:"CRM", items:[funil]}]`. Componente renderiza subgrupos colapsáveis dentro do grupo pai. Itens do bot que hoje estão soltos em "WhatsApp" migram para o subgrupo "IA".

## Não-objetivos (YAGNI)
- Chat dentro do CRM (atendimento é no WhatsApp).
- Campos extras (origem, tags), automações além do tie-in.
- Recurso de cargo novo (reusa `whats`).

## Plano de implementação
1. bot-go: migration `0019` (normaliza status) + `LeadRepo` (List/UpdateStatus/SetStatusByConversation) + criar lead no inbound + endpoints GET/PATCH `/api/leads` + rota no server + tie-in no booking confirm. Build.
2. dashboard: nav com subgrupos + componente sidebar renderizando subgrupos colapsáveis. Lint/build.
3. dashboard: `whats.ts` (leads) + página Kanban `WhatsCRM.tsx` + rota em App.tsx. Lint/build.
4. Commit/push/deploy (bot-go + dashboard-web).
