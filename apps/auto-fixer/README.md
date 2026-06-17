# auto-fixer

Serviço que escuta webhooks de **falha de deploy da Coolify**, roda o **Claude Code**
num clone do repo para consertar o build, dá **push na branch principal** e reporta no
grupo do WhatsApp usando a mensagem de log como âncora (reação de status + reply com o
diagnóstico).

Roda no **host Contabo, fora da Coolify** (via docker-compose próprio), para continuar de
pé mesmo quando o build que ele conserta — ou a própria Coolify — está quebrado.

## Fluxo

```
Coolify (deploy.failed) ─webhook→ auto-fixer
  → incident:create  posta "🔧 Build de X falhou…" no grupo + reage 🔧
  → fix:run          clona, lê logs de build, roda Claude, commita+push (branch principal)
  → (aguarda o 2º webhook da Coolify, o do rebuild — sem polling)
  → notify:final     reação ✅/💀 + reply com o que foi corrigido
```

Loop-guard: `MAX_FIX_ATTEMPTS` (default 2) por incidente; a mensagem de commit do fix
leva `[skip auto-fix]`, então o rebuild dela não re-dispara o ciclo. `deploy:timeout`
(default 15 min) cobre o rebuild que nunca confirma (reação ⏰).

## Rodar local

```bash
cp .env.example .env   # preencha os tokens
docker compose up --build
# health: curl localhost:3340/health
```

Sem Docker, para desenvolver:

```bash
PATH=$PATH:$HOME/.local/bin go test .   # unitários (httptest + miniredis)
```

## Apontar o webhook da Coolify

Na Coolify, em cada aplicação (ou global) → **Notifications / Webhooks** → adicione:

```
URL: https://<host-do-fixer>/webhooks/coolify?token=<COOLIFY_WEBHOOK_SECRET>
Eventos: deployment (sucesso e falha)
```

O fixer recebe **tanto** a falha (abre incidente) **quanto** o sucesso (fecha o incidente
quando o rebuild do fix passa).

## Confirmar o shape real da Coolify (antes do e2e)

O parser dos logs de build e do repo do app assume um formato; confirme com a sua Coolify:

```bash
# logs de BUILD do deployment (campo "logs" → [{"output": "..."}])
curl -H "Authorization: Bearer $COOLIFY_API_TOKEN" \
  "$COOLIFY_API_URL/api/v1/deployments/<deployment_uuid>" | jq '.logs' | head

# repo e branch do app (campos git_repository / git_branch)
curl -H "Authorization: Bearer $COOLIFY_API_TOKEN" \
  "$COOLIFY_API_URL/api/v1/applications/<app_uuid>" | jq '{git_repository, git_branch}'
```

Se o formato divergir, ajuste `coolify.go` (`BuildLogs`/`AppRepo`). O `BuildLogs` cai
para "log cru" se o JSON não casar, então degrada com graça.

## Segurança

- O Claude roda com `--dangerously-skip-permissions` sobre **input não confiável** (logs
  de build). Mitigações: o `GITHUB_TOKEN` e demais segredos do fixer **não** entram no
  ambiente do processo do Claude (allowlist em `claudeEnv`, `claude.go`); o git
  (clone/commit/push) é todo do orquestrador via askpass com token escapado; argv do git
  validado contra argument-injection.
- **Hardening pendente** (ver plano, Tasks 10–11): token efêmero escopado ao repo (GitHub
  App) e execução do Claude em container `docker run --rm` descartável por incidente.
- O `docker-compose.yml` **não** referencia a rede da Coolify de propósito.

## Estrutura

`config.go` · `coolify_parse.go` (classificação do webhook) · `coolify.go` (API) ·
`evolution.go` (reação/reply) · `incident.go` (estado no Redis) · `jobs.go` + `webhook.go`
(fila/handler) · `claude.go` + `gitops.go` (motor de fix) · `workers.go` (4 jobs) ·
`main.go` (boot). Plano completo: `../../docs/superpowers/plans/2026-06-16-auto-fixer.md`.
