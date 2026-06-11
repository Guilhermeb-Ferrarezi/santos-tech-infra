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
- [x] Integrar no email-sender: verifica JWT do auth central (trocou senha+Redis) — testado
- [x] Painel frontend: usar login central (cookie + redirect, sem senha)
- [x] **Deploy do auth Go em produção** — `santos-tech-api-go` no Easypanel; cutover de `api.santos-tech.com` (updateDomain, sem gap). Validado: register/login/me/401 contra o DB de produção.
- [x] **Redeploy email-sender** (código completo + DATABASE_URL/REDIS_URL/JWT_SECRET) — produção: health 200, /send intacto, /leads com JWT central → 200

## ✅ CONCLUÍDO — auth em Go reescrita, deployada (`api.santos-tech.com`) e integrada no email service. Verificado em produção.

**Rollback:** a api TS (`santos-tech-api`) segue rodando sem domínio; é só apontar `api.santos-tech.com` de volta pra ela no Easypanel.

## Notas
- Endpoints atuais: register, login (email|username), logout, me, refresh, forgot/reset-password, GET /auth/google + callback.
- Erro: `{code, message}` + status. Cookies: access_token/refresh_token, httpOnly, sameSite lax, domain COOKIE_DOMAIN.
- Roles: 1=Student 2=Teacher 3=Admin 4=Custom (custom_roles.permissions jsonb).

## 2026-06-09 — Portal admin/professor API Phase 1 (`/portal/*`)
Primeira fatia da migração do portal pra API Go central: overview, catálogo
(cursos/módulos/fases), turmas, matrículas e salas. Mantém o schema Postgres
atual sem alteração, leitura por staffGuard e escrita por adminGuard (DELETE com
sudo). Content/submissions/badges/goals e a desativação da API TS antiga ficam
para planos seguintes.

## 2026-06-10 — Portal API Go Fase 2 (conteúdo)
Exercícios (CRUD + reorder + by-phase + daily-tasks + múltipla escolha via
question/question_option, 409 se já há respostas), containers (container_tasks),
materiais e vídeos (CRUD JSON; módulo embutido na description como [[module:id|nome]];
URL pré-enviada via /auth/upload). Schema preservado. Falta: submissões/progresso,
badges, goals, notificações (fases 3-5) e desativar a API TS antiga.
