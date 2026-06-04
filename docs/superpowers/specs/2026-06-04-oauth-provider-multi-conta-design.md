# OAuth Provider ("Entrar com Santos Tech") + Multi-conta no navegador

**Data:** 2026-06-04
**Repo:** santos-tech-infra
**Status:** aprovado em brainstorming, aguardando plano de implementação

## Objetivo

1. Transformar o auth central (`apps/api-go`) em um **provedor OAuth 2.0**
   (authorization code + PKCE) para que apps do ecossistema mostrem um botão
   "Entrar com Santos Tech".
2. Suportar **múltiplas contas logadas no mesmo navegador** (estilo Google),
   com escolha de conta na tela de autorização do `auth-web`.

## Escopo e decisões

- **Consumidores:** só apps internos por ora. Sem tela de consentimento, sem
  `client_secret` — PKCE (S256) obrigatório protege o code.
- **Troca de conta:** acontece na tela de autorização (chooser). Cada app fica
  com a conta escolhida no fluxo OAuth.
- **Abordagem escolhida:** implementar o protocolo direto no `api-go`
  (authorization code + PKCE enxuto), reusando JWTs HS256, tabela `sessions`
  e Redis existentes. Descartadas: biblioteca OIDC completa (pesada, conflita
  com a sessão atual) e SSO por ticket proprietário (não é OAuth, sem caminho
  para terceiros).
- **Coerência com o ecossistema:** o `JWT_SECRET` é compartilhado, então o
  access token emitido via OAuth é validado pelos serviços exatamente como o
  token de cookie. OAuth é só mais uma porta de entrada para a mesma sessão.

## Parte 1 — Multi-conta no navegador

**Hoje:** 1 par de cookies (`access_token` 2h + `refresh_token` 7d, domínio
`.santos-tech.com`) = 1 conta ativa. Cada login cria uma linha em `sessions`
(Postgres: `id`, `user_id`, `refresh_token_hash`, `expires_at`).

**Mudanças:**

1. **Novo cookie `accounts`** — httpOnly, `Domain=.santos-tech.com`, assinado
   com HMAC (usa o `JWT_SECRET`). Conteúdo: lista de até **5 `session.id`s**
   (UUIDs). É um índice de "contas conhecidas neste navegador"; não dá acesso
   por si só — uso/ativação sempre passa pela sessão real no banco.
2. **Login anexa em vez de substituir** — login (senha, Google ou MFA) segue
   setando `access_token`/`refresh_token` (conta ativa) e adiciona o
   `session.id` novo ao cookie `accounts`. Zero quebra do fluxo atual.
3. **`GET /auth/accounts`** — lê o cookie, valida cada `session.id` no
   Postgres, faz join com `users` e devolve
   `[{ sessionId, name, email, avatarUrl, active }]`.
4. **`DELETE /auth/accounts/{sessionId}`** — remove da lista e apaga a sessão
   no Postgres ("remover conta" no chooser).
5. **`POST /auth/accounts/{sessionId}/activate`** — troca a conta ativa no
   auth-web: rotaciona a sessão escolhida (novo refresh, como `/auth/refresh`)
   e seta os cookies ativos.
6. **Logout** — limpa cookies ativos, apaga a sessão do Postgres e a remove do
   cookie `accounts`. As demais contas permanecem no chooser.

**Auto-limpeza:** qualquer endpoint que toque o cookie `accounts` e encontre
sessão morta (expirada/revogada) **regrava o cookie sem ela**. Revogação no
banco mata a conta no chooser imediatamente.

## Parte 2 — Fluxo OAuth provider

### Registro de aplicações

Nova tabela via `migrate()` no `db.go` (idempotente, padrão da migração MFA):

```sql
oauth_clients (
  id uuid PK,
  client_id text UNIQUE,
  name text,
  redirect_uris text[],
  is_active bool,
  created_at timestamptz
)
```

Gestão via rotas admin (`adminGuard`), padrão `handlers_admin_users.go`.

### Fluxo (authorization code + PKCE S256)

1. **`GET /oauth/authorize?client_id&redirect_uri&state&code_challenge&code_challenge_method=S256`**
   - Valida client ativo + `redirect_uri` na allowlist (match exato).
   - Guarda a requisição no Redis (`oauth:authreq:<id>`, TTL 10min).
   - Redireciona para `AUTH_WEB_ORIGIN/oauth/choose?request_id=<id>`.
   - Sem nenhuma conta no navegador → login normal e volta ao chooser.
2. **`POST /oauth/authorize/confirm`** `{ requestId, sessionId }`
   - Confere que o `sessionId` está no cookie `accounts` e vivo no Postgres.
   - Sessão morta → `401 { code: "session_expired" }` + regrava o cookie sem
     ela; o front volta ao chooser preservando o `request_id`.
   - Gera o **code**: 32 bytes aleatórios; Redis `oauth:code:<sha256>` com
     `{ clientId, redirectUri, userId, codeChallenge }`, TTL 60s, uso único.
   - Devolve `{ redirectTo: "<redirect_uri>?code=...&state=..." }`.
3. **`POST /oauth/token`** (público, rate limit 20/min)
   - `grant_type=authorization_code` + `code` + `client_id` + `redirect_uri`
     + `code_verifier`.
   - Valida tudo, consome o code atomicamente (`GETDEL`), emite os mesmos
     JWTs de hoje (`generateTokens`) e cria linha em `sessions`.
   - Resposta: `{ access_token, refresh_token, token_type: "Bearer", expires_in }`.
   - `grant_type=refresh_token` também aceito (mesma rotação do `/auth/refresh`).
4. **Userinfo:** sem endpoint novo — `GET /auth/me` com `Authorization: Bearer`
   já resolve perfil, role e permissões.

### Erros

- Convenção do repo: `{ code, message }` em português, status HTTP adequado.
- No `/oauth/authorize`: se o `redirect_uri` for válido, erros voltam por
  redirect (`?error=...&state=...`, conforme a spec OAuth); se o próprio
  client/redirect for inválido, erro JSON direto — **nunca** redirecionar
  para URI não confiável.

## Parte 3 — Frontend (auth-web)

1. **Nova rota `/oauth/choose?request_id=...`** (React 19 + TanStack
   Router/Query + Tailwind 4):
   - `GET /auth/accounts` → cards com avatar, nome, e-mail (identidade visual:
     fundo `#F5F8FA`, CTA teal `#0DB88F`, cards arredondados).
   - Clique → `POST /oauth/authorize/confirm` → `window.location.href = redirectTo`.
   - "Usar outra conta" → login atual preservando `request_id`; após logar,
     volta ao chooser.
   - Remover conta da lista → `DELETE /auth/accounts/{sessionId}`.
2. **Fluxo de login** aceita `request_id`/`returnTo` para voltar ao chooser
   após o login — incluindo caminho com MFA e callback do Google.
3. **Caso de borda:** `request_id` expirado (10min) → mensagem "sessão de
   autorização expirou", orientando a refazer o login a partir do app de
   origem (sem o request não há URL de retorno confiável).

## Testes

- **Go (`httptest`, unitários):** validação de client/redirect_uri; PKCE
  (verifier correto/errado/ausente); code de uso único (segunda troca falha);
  code expirado; confirm com sessão morta (limpa cookie + 401); pruning do
  cookie `accounts`; limite de 5 contas.
- **Integração (Postgres + Redis reais):** authorize → confirm → token →
  `/auth/me` com Bearer.
- **Frontend:** `bun run build` (type-check + build de produção).

## Documentação obrigatória (mesmo commit)

- `docs/openapi.yaml` — novas rotas.
- `apps/api-go/llms.txt` — contrato atualizado para agentes/devs.
