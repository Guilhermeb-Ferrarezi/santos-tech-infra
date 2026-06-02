# Auth API em Go — progresso

Reimplementação da `apps/api` (Fastify/TS) em Go + integração no serviço de email.

## Decisões
- Stack: Go stdlib net/http + pgx + go-redis + golang-jwt/v5 (HS256, mesmo `JWT_SECRET`) + argon2id (compatível Bun.password) + x/oauth2 (Google) + pquerna/otp (MFA).
- Reusa o **Postgres atual** (tabelas users/sessions/oauth_accounts/custom_roles já existem). Migration nova só pro MFA.
- Emails (reset, MFA por email) → **nossa API** (`POST mails.santos-tech.com/api/send`), não Resend.
- Logs: stdout estruturado (sem Mongo).
- MFA: TOTP (Google Authenticator) + código por email + códigos de recuperação.
- Substitui a TS no fim. Compat: mesmo JWT/cookies em `.santos-tech.com` → painel de email verifica o mesmo token.

## Checklist
- [x] Scaffold + deps (go.mod)
- [x] Fundação: config, models, password (argon2), token (jwt), email client, db (pgx), redis, server skeleton, main, /health — **testado contra DB/Redis reais**
- [x] Migration MFA (mfa_enabled, totp_secret, recovery_codes) — **aplicada no banco santos-tech**
- [x] Core auth: register, login, logout, me, refresh, authGuard — **testado contra DB real (argon2+JWT+sessões+cookies OK)**

> Infra real: DB `cloud.santos-tech.com:8536/santos-tech`, Redis `:9234`. Email via `mails.santos-tech.com/api`.
- [x] Password reset: forgot/reset (redis token + email via nossa API) — compila
- [x] OAuth Google — **redirect + state CSRF testados**
- [x] MFA: setup/enable/disable (TOTP), email-OTP, recovery codes, login 2 passos — **testado (TOTP+recovery OK)**

**API de auth em Go COMPLETA** (core + reset + MFA + OAuth). Falta deploy + integração no email-sender.
- [x] Build + testar contra DB/Redis reais — **todos os blocos testados**
- [ ] Deploy no Easypanel (substitui a TS)
- [x] Integrar no email-sender: verificar JWT do auth central (trocou senha+Redis) — **testado: Bearer/cookie central → 200**
- [x] Painel frontend: usar login central (cookie + redirect, sem senha) — compila
- [ ] Redeploy email-sender com JWT_SECRET (+ painel) — **precisa da URL do auth web e confirmação**
- [ ] Deploy do auth Go substituindo a TS — **produção, precisa de confirmação**

## Artefatos de deploy prontos
Dockerfile (multi-stage → distroless), .env.example, .gitignore.

## ⚠️ Passos de produção (precisam de input/confirmação do usuário)
1. Qual a URL do **auth web** (página de login do santos-tech)? Precisa pra `VITE_AUTH_URL` (painel) + `AUTH_WEB_ORIGIN`.
2. Deploy do auth Go **substitui a api TS de produção** (afeta o login de TODAS as apps santos-tech) — confirmar antes.
3. Adicionar `JWT_SECRET` no env do email-sender no Easypanel + redeploy email-sender + painel.

## Notas
- Endpoints atuais: register, login (email|username), logout, me, refresh, forgot/reset-password, GET /auth/google + callback.
- Erro: `{code, message}` + status. Cookies: access_token/refresh_token, httpOnly, sameSite lax, domain COOKIE_DOMAIN.
- Roles: 1=Student 2=Teacher 3=Admin 4=Custom (custom_roles.permissions jsonb).
