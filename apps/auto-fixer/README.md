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
- **Token efêmero (Task 10)**: o git usa um installation token do GitHub App escopado ao
  repo do incidente (TTL ~1h), com fallback pro PAT. Requer o App instalado nos owners.
- **Sandbox do Claude (Task 11)**: com `CLAUDE_SANDBOX=docker`, o Claude roda num container
  `docker run --rm` descartável por incidente — só recebe o OAuth (nenhum segredo do fixer),
  workspace por volume, rede opcionalmente restrita (`CLAUDE_DOCKER_NETWORK`). Exige o socket
  Docker montado (ver `docker-compose.yml`). Vazio = exec direto. **A execução real (imagem,
  volume, socket) é validada no e2e.**
- **Webhook fail-closed**: `COOLIFY_WEBHOOK_SECRET` é obrigatório no boot e o handler
  nega quando ele está vazio (antes, sem a env, o webhook virava anônimo).
- **Allow-list de repositórios**: `ALLOWED_REPOS` (CSV `org/repo`, obrigatório) limita o
  que o fixer pode clonar com o token da organização. Vazio = nega tudo.
- **Teto do fix**: o commit automático é abortado se tocar caminho protegido
  (`.github/workflows/**`, `**/Dockerfile*`, `infra/**`, `.git/**`) ou passar de 20
  arquivos / 800 linhas alteradas — o push vai direto na branch de deploy, sem review.
- **Workdir**: nome do app e id do incidente são validados (`^[A-Za-z0-9._-]+$`, sem
  `.`/`..`/hífen inicial) e o destino precisa ficar sob o `WORKSPACE_ROOT`. O clone é
  removido ao fim de cada run.
- O `docker-compose.yml` **não** referencia a rede da Coolify de propósito.

## Estrutura

`config.go` · `coolify_parse.go` (classificação do webhook) · `coolify.go` (API) ·
`evolution.go` (reação/reply) · `incident.go` (estado no Redis) · `jobs.go` + `webhook.go`
(fila/handler) · `claude.go` + `gitops.go` (motor de fix) · `workers.go` (4 jobs) ·
`main.go` (boot). Plano completo: `../../docs/superpowers/plans/2026-06-16-auto-fixer.md`.
