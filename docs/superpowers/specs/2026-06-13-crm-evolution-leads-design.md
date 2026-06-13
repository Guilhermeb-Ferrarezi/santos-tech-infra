# CRM — captura de leads do número da Evolution

**Data:** 2026-06-13 · **Projetos:** `apps/bot-go` + `Santos-Techrp/dashboard` · **Contexto:** teste

## Objetivo

Integrar o número conectado pela **Evolution API** (Baileys) ao CRM: toda mensagem
recebida nesse número vira um **lead** no funil, marcado com **origem `evolution`**.
**Só captura** — sem auto-resposta (atendimento manual no WhatsApp).

## Decisões (alinhadas)
- Escopo: **só capturar leads** (sem bot respondendo no número da Evolution).
- **Marcar a origem**: cada lead distingue `oficial` (bot Cloud API) de `evolution`.
- É **teste** — número pessoal "Guilherme"; aceito que mensagens pessoais virem lead.

## Arquitetura

### Fluxo
1. A instância Evolution "Guilherme" é configurada para enviar o evento de mensagem
   recebida (`MESSAGES_UPSERT`) para `POST https://api.santos-tech.com/bot/webhooks/evolution`,
   com um header de segredo.
2. O bot-go valida o segredo, ignora grupos (`@g.us`), status e mensagens próprias
   (`fromMe`), extrai o telefone do remetente e o `pushName`, e cria/atualiza o lead
   com `origin = 'evolution'`. Não responde nada.

### bot-go
- `migrations/0020_lead_origin.sql`: `ALTER TABLE lead ADD COLUMN origin text NOT NULL DEFAULT 'oficial'`.
- `config.go`: `EvolutionWebhookSecret` (env `EVOLUTION_WEBHOOK_SECRET`).
- `server.go`: rota `POST /webhooks/evolution` (sem dash-auth; valida o segredo no header) + `withTenant`/repos no Server.
- `handler` novo `handleEvolutionWebhook`: parseia o payload da Evolution, filtra, e captura o lead.
- `repos.go`: `LeadRepo.CreateWithOrigin(ctx, tx, tenantID, contactID, origin)`; reuso de `ContactRepo.FindByChannelIdentity`/`CreateWithChannelIdentity` (channel `whatsapp`, external_id = telefone) dentro de um `withTenant`.
- `handlers_dash.go`: `GET /api/leads` passa a retornar `origin`.

### Dashboard
- `whats.ts`: `BotLead.origin`.
- `WhatsCRM.tsx`: selo de origem no card (**Oficial** / **Evolution**); opcional filtro por origem.

### Config da Evolution (pós-deploy)
- `POST {evolutionURL}/webhook/set/Guilherme` com `url`, `events: [MESSAGES_UPSERT]`, `webhook_by_events: false`, e header de segredo.

## Não-objetivos (YAGNI)
- Bot respondendo no número da Evolution.
- Importar histórico anterior.
- Múltiplas instâncias Evolution (uma só por ora).

## Plano de implementação
1. bot-go: migration 0020 + config + `CreateWithOrigin` + handler + rota + `origin` no GET /api/leads + wiring repos/withTenant no Server. Build.
2. dashboard: origin no `BotLead` + selo no card. Lint/build.
3. Commit/push/deploy (bot-go + dashboard-web).
4. Setar `EVOLUTION_WEBHOOK_SECRET` no bot e configurar o webhook na Evolution. Testar mandando msg pro número.
