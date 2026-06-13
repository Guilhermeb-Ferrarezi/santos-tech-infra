# Evolution — status dos números + bot responde (toggle, off por padrão)

**Data:** 2026-06-13 · **Projetos:** `apps/bot-go` + `Santos-Techrp/dashboard`

## Objetivo
1. **Ver os WhatsApp conectados** (instâncias da Evolution) com status.
2. **Interruptor "Bot responde"** no número da Evolution — **desligado por padrão**:
   off = só capta lead; on = bot responde com a mesma IA via Evolution.

## Backend (bot-go)
- Envs: `EVOLUTION_API_URL`, `EVOLUTION_API_KEY`, `EVOLUTION_INSTANCE`.
- `migrations/0021`: `ALTER TYPE channel ADD VALUE 'evolution'` (no-transaction).
- `migrations/0022`: `tenant_config.evolution_bot_reply_enabled bool default false`.
- `evolution.go`: `EvolutionSender` (implementa `ChatSender`: SendMessage/SendText via `POST {url}/message/sendText/{instance}`; SendTypingIndicator = no-op) + `EvolutionAdmin.Instances()` (proxy de `/instance/fetchInstances`).
- `main.go`: cria `EvolutionSender` + um **segundo engine** (`evoEngine`) com os mesmos repos mas `Sender = EvolutionSender`.
- Webhook `/webhooks/evolution`: capta o lead (channel **`evolution`**) SEMPRE; se a flag estiver ON e houver texto, monta InboundMessage (Channel=`evolution`) e chama `evoEngine.Handle` → resposta via Evolution.
- Dashboard API: `GET /api/evolution/instances` (proxy status); `evolutionBotReplyEnabled` no GET/PATCH `/api/config`.

## Dashboard
- Nav: item **"Números (Evolution)"** no grupo WhatsApp (seção CRM).
- Página: lista instâncias + status (🟢/🟡/🔴) + toggle "Bot responde automaticamente" (default off).
- `whats.ts`: `bot.evolutionInstances()`; `BotConfig.evolutionBotReplyEnabled`.

## Decisões / YAGNI
- Captura de lead já pronta — muda só o channel para `evolution` (consistência).
- Uma instância Evolution (`EVOLUTION_INSTANCE`). Sem criar instância/QR pela tela.
- Notificações de admin (handoff/gap) seguem pela Meta (worker), não pela Evolution.

## Plano
1. Migrations 0021/0022 + config envs.
2. `evolution.go` (sender + instances).
3. main.go: EvolutionSender + evoEngine; Server recebe evoEngine + EvolutionAdmin.
4. Webhook: channel evolution + roteia p/ evoEngine quando flag on.
5. Config GET/PATCH com a flag; `GET /api/evolution/instances`.
6. Dashboard: página + nav + whats.ts + toggle.
7. Build, deploy, setar envs Evolution no bot, testar.
