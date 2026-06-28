# agent-go

## ⚠️ OBRIGATÓRIO — verificar build e lint antes de commitar/pushar/deployar

**NUNCA** commite, faça push ou dispare deploy sem antes rodar a verificação de **build**
e **lint** e confirmar que passam **sem erros**. Build quebrado = deploy quebrado.

- **Frontend (React/Vite):** `bun run lint` **e** `bun run build` (o build faz o type-check `tsc`).
- **Go:** `gofmt -l .` (saída vazia) · `go vet ./...` · `go build ./...` · `go test ./...`.

Se qualquer etapa falhar, **corrija antes de prosseguir** — não pushe "pra ver se passa no CI".


API em Go que **orquestra o Claude Code** rodando dentro do container. Faz spawn do
`claude` CLI (modo `--dangerously-skip-permissions`), expõe um chat por **WebSocket**,
persiste conversas no Postgres compartilhado e dá ao agente acesso a GitHub (MCP),
Easypanel e Cloudflare. Exposto em `api.santos-tech.com/claude/*`.

> ⚠️ **Serviço privilegiado por design.** O agente roda Claude sem checagem de
> permissões, com bash e tokens de cloud no ambiente — ou seja, **pode executar
> qualquer coisa** no container e na infra. A proteção é o `authGuard` **admin-only**
> (role=3) + auditoria (toda mensagem/tool call é persistida). Trate `JWT_SECRET`,
> `ENCRYPTION_KEY` e os tokens de infra como segredos críticos.

## Modelo de orquestração (HÍBRIDO: sessão viva + por-turno)

O motor é escolhido **por conversa**, no handler do WebSocket, conforme `ToolsDisabled`
(`DispatchPrompt`/`DispatchInterrupt` em `session.go`):

| Conversa | Motor | Onde |
|----------|-------|------|
| **Admin** (ferramentas habilitadas) | **sessão viva** | `livesession.go` / `livepool.go` |
| **Terceiros / WhatsApp** (`tools_disabled=true`) | **por-turno** | `RunTurn` em `session.go` |

> Por quê: o whats-agent abre **um WS por mensagem** em muitas conversas de baixa
> frequência (1 por contato). No motor vivo isso seguraria um processo `claude` por
> contato (pool `CLAUDE_MAX_LIVE`, default 4) → thrashing. O por-turno (spawn→roda→morre)
> escala melhor pra esse perfil. O motor vivo é pro uso admin interativo.

### Motor de sessão viva (admin)

Um processo `claude` **de longa duração por conversa**, alimentado por stdin em streaming.
Fica vivo entre turnos (sem cold start), mantém estado em memória e permite mandar
mensagem / parar enquanto trabalha.

```
claude -p --output-format stream-json --verbose --include-partial-messages \
  --input-format stream-json --replay-user-messages \
  --dangerously-skip-permissions --model <model> --add-dir <workdir> \
  [--mcp-config <workdir>/.mcp.json --strict-mcp-config] \
  ( --session-id <session_id>  |  --resume <session_id> )
# mensagens do usuário entram como JSON no stdin: {"type":"user","message":{...}}
# eventos JSONL saem pelo stdout; control_request de interrupt = "parar" sem matar o processo
```

- **Pool** com cap `CLAUDE_MAX_LIVE` (default 4) + evict LRU de sessões ociosas.
- **Hibernação por idle** (`CLAUDE_IDLE_TTL`, default 15m): fecha o stdin (CLI salva a
  sessão) e remove do pool; a próxima mensagem **ressuscita via `--resume`**.
- **Fila**: mensagem durante um turno entra na fila; **botão parar** (`interrupt` no WS)
  manda `control_request` e encerra o turno sem matar o processo.
- `markSessionStarted` é persistido após o 1º turno → a ressurreição usa `--resume`.

### Motor por-turno (WhatsApp / `tools_disabled`)

Não há daemon: cada turno faz spawn de um processo que **retoma a sessão do disco**,
executa e sai (`RunTurn`/`exec` em `session.go`). Sem `--input-format stream-json`,
sem `--dangerously-skip-permissions` (gate por allow-list vazia), prompt vai como texto
pelo stdin.

```
claude -p --output-format stream-json --verbose --include-partial-messages \
  --model <model> [--allowed-tools ...] --add-dir? \
  ( --session-id <session_id>  |  --resume <session_id> )
```

### Comum aos dois motores

- `conversation.id` = PK estável (URLs, FK das mensagens).
- `conversation.session_id` = `--session-id` do Claude, **rotacionável** por `/clear` e
  `/compact` sem perder o histórico no banco.
- 1º turno da sessão usa `--session-id`; os seguintes usam `--resume` (controlado por
  `session_started`).
- Slash commands (`/model`, `/compact`, `/clear`) **não existem** em modo `-p` → são
  operações na camada de orquestração (no motor vivo, `Evict()` reinicia o processo p/
  aplicar):
  - `/model`: grava o modelo; aplica no próximo turno.
  - `/compact`: roda um turno de resumo, rotaciona a sessão e deixa o resumo como
    *seed* (Redis) do próximo turno. Rejeita com `BUSY` (409) se há turno em andamento.
  - `/clear`: rotaciona a sessão (contexto zerado).

## Stack

`net/http` stdlib · `pgx/v5` (Postgres) · `go-redis/v9` (lock de turno, estado, rate
limit, seed) · `golang-jwt/v5` (valida o JWT do auth central) · `coder/websocket` (chat)
· `creack/pty` (fluxo OAuth do `claude setup-token`) · AES-256-GCM (token OAuth em
repouso) · `slog`. Mesmas convenções do `apps/api-go` (erros `{code,message}`, CORS,
rate limit por rota+IP).

## Endpoints (sob `/claude`, todos admin exceto health)

| Método | Rota | Descrição |
|--------|------|-----------|
| GET | `/claude/health` | liveness (sem auth) |
| GET | `/claude/conversations` | lista do usuário |
| POST | `/claude/conversations` | cria `{title?, repo?, model?}` → workdir + `.mcp.json` + clone |
| GET | `/claude/conversations/{id}` | detalhe + mensagens |
| DELETE | `/claude/conversations/{id}` | remove + limpa workdir |
| GET | `/claude/conversations/{id}/ws` | **WebSocket** de chat |
| POST | `/claude/conversations/{id}/model` | troca modelo `{model}` |
| POST | `/claude/conversations/{id}/compact` | compacta contexto |
| POST | `/claude/conversations/{id}/clear` | zera contexto |
| POST | `/claude/generate` | geração one-shot stateless `{task, brief, tone?}` → `{subject, html, text}` |
| POST | `/claude/auth/login` | inicia OAuth (PTY) → `{state, authUrl}`; ou `{token}` direto |
| POST | `/claude/auth/callback` | `{state, code}` → captura e cifra o token |
| POST | `/claude/auth/logout` | limpa o token |
| GET | `/claude/auth/status` | `logged_in` / `logged_out` |

**WebSocket** — cliente envia `{type:"prompt", text}` ou `{type:"interrupt"}`; servidor
emite `init` · `delta` (texto ao vivo) · `tool_use` · `tool_result` · `result` ·
`error` · `busy` · `done`.

**Auth** — autentica via JWT de sessão (cookie/Bearer) **ou** Personal Access Token do auth
(`Authorization: Bearer st_…`, validado na tabela `api_keys` compartilhada). As rotas
privilegiadas (conversas, controle, OAuth) exigem **admin**. **Exceção:** `POST /claude/generate`
aceita **qualquer usuário autenticado** — é **stateless** e roda o Claude em sandbox: só o OAuth
da assinatura, **sem** `--add-dir`, MCP, `--dangerously-skip-permissions` ou tokens de infra,
então não precisa do papel admin.

## Rodar (local)

```bash
# bancos
docker compose -f infra/docker-compose.yml up -d postgres redis
# precisa da tabela `users` (criada pelo auth) + um usuário role=3 (Admin)

cd apps/agent-go && go run .   # exige o `claude` CLI no PATH
```

Migrações (`claude_conversations`, `claude_messages`, `claude_credentials`) rodam no boot
(`migrate()` em `db.go`, idempotente). A tabela `users` é compartilhada e não é criada aqui.

## Variáveis de ambiente

`.env.example` é a referência. Obrigatórias: `DATABASE_URL`, `REDIS_URL`, `JWT_SECRET`
(igual ao auth), `ENCRYPTION_KEY`. Runtime do agente: `CLAUDE_BIN`, `CLAUDE_DEFAULT_MODEL`,
`CLAUDE_WORKSPACE_ROOT`, `GITHUB_TOKEN`, `EASYPANEL_URL/TOKEN`, `CLOUDFLARE_API_TOKEN`.

## Deploy

`infra/Dockerfile.agent-go` (node:22-slim + `claude` CLI + git; **não** distroless).
**Volumes persistentes obrigatórios:** `/home/agent/.claude` (credenciais OAuth + sessões
`.jsonl` do `--resume`) e `/data/workspaces` (checkouts). No Easypanel, rotear
`PathPrefix(/claude)` para este serviço (sem strip — as rotas já são `/claude/*`).

## Pré-commit

```bash
cd apps/agent-go
gofmt -l .     # vazio
go vet ./...
go build ./...
go test ./...  # unitários; não precisam de Postgres/Redis
```

## A verificar contra o CLI real (`claude` v2.x)

O fluxo OAuth via PTY (`handlers_auth.go`) depende do formato de saída do
`claude setup-token` (URL e token impressos). As regex de extração (`urlRe`/`tokenRe`)
podem precisar de ajuste — há fallback de `{token}` direto no `/auth/login`. Também
confirmar `--session-id`/`--resume` e o schema exato dos eventos stream-json.

## Documentação central (llms.txt)

Alterou rotas `/claude/*`? Atualize a seção "Claude Agent" do
`../api-go/llms.txt` no mesmo commit — é a doc viva (autenticada) que os agentes
leem em `https://api.santos-tech.com/llms.txt`.
