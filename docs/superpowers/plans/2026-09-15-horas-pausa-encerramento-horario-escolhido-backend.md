# Horas — escolher horário real ao pausar/encerrar (back-end) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Corrigir o bug relatado pelo Henrique (pausa aceita horas depois cobra o
tempo todo do cliente) e generalizar a solução: `pause`/`end` passam a aceitar um
horário explícito e validado, e o cliente ganha "Já vou embora" (pedido de
encerramento, simétrico ao "Pedir pausa" que já existe) — back-end da spec do
repo `dashboard`
(`docs/superpowers/specs/2026-09-15-horas-pausa-usa-horario-solicitacao-design.md`).

**Architecture:** Estende `hour_sessions.go`/`handlers_hour_sessions.go` (mesmo
domínio já usado por start/pause/resume/end) — nenhum arquivo novo. `pause_requested_at`
ganha um irmão, `end_requested_at`, com o mesmo padrão de pedido/recusa. A escolha
de horário é um campo opcional (`at`) no corpo de `pause`/`end`; quando presente,
validado contra o servidor por uma função pura (`validateEventAt`, testável sem
banco) antes de ser gravado como `created_at` do evento.

**Tech Stack:** Go 1.25, `pgx/v5`, `net/http` stdlib, testes puros (sem
`httptest`/DB — mesmo padrão já usado em `hour_sessions_test.go`, que só testa
funções sem I/O).

---

## Pré-requisito

Checkout em `C:\Users\55169\Documents\GitHub\santos-tech-infra` está na branch
`claude-henrique/aula-count-por-encontro` (working tree limpo, sincronizada com
seu remote — não mexer nela). Criar branch nova a partir do `master` remoto
fresco:

```bash
cd "C:\Users\55169\Documents\GitHub\santos-tech-infra"
git fetch origin master
git checkout -b claude-henrique/horas-pausa-encerramento-horario-escolhido origin/master
```

Rode os comandos a partir de `apps/api-go` dentro desse checkout, salvo indicação
contrária.

---

### Task 1: Migration — `hour_sessions.end_requested_at`

**Files:**
- Modify: `apps/api-go/db/schema.sql` (bloco de `ALTER TABLE hour_sessions`, logo após a linha do `scheduled_start_at`)

- [ ] **Step 1: Acrescentar a coluna**

Hoje, logo depois da criação de `hour_session_events` (schema.sql, por volta da
linha 548), o bloco de ALTERs de `hour_sessions` termina em:

```sql
ALTER TABLE hour_sessions ADD COLUMN IF NOT EXISTS scheduled_end_at TIMESTAMPTZ;
ALTER TABLE hour_sessions ADD COLUMN IF NOT EXISTS scheduled_start_at TIMESTAMPTZ;
```

Acrescente logo abaixo:

```sql
-- end_requested_at: pedido do cliente pra encerrar de vez (rota pública "Já
-- vou embora"), espelha pause_requested_at — quem decide encerrar de fato
-- continua sendo o admin, que pode usar esse horário como referência.
ALTER TABLE hour_sessions ADD COLUMN IF NOT EXISTS end_requested_at TIMESTAMPTZ;
```

- [ ] **Step 2: Build**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro (idempotente, roda no próximo deploy real — sem Postgres
local nesta máquina pra testar o boot da migration).

- [ ] **Step 3: Commit**

```bash
git add apps/api-go/db/schema.sql
git commit -m "feat(hour): adiciona hour_sessions.end_requested_at"
```

---

### Task 2: Ler/expor `end_requested_at`

**Files:**
- Modify: `apps/api-go/hour_sessions.go`

- [ ] **Step 1: Campo no struct `HourSession`**

Em `hour_sessions.go`, o struct hoje (linhas 31-51) tem `PauseRequestedAt`.
Acrescente logo abaixo:

```go
	PauseRequestedAt *time.Time `json:"pauseRequestedAt"`
	// EndRequestedAt: pedido do cliente pra encerrar de vez (rota pública "Já
	// vou embora"). Espelha PauseRequestedAt — quem decide encerrar
	// continua sendo o admin.
	EndRequestedAt   *time.Time `json:"endRequestedAt"`
```

- [ ] **Step 2: Coluna na query e no Scan**

`hourSessionCols` (linha 166-167) hoje:

```go
const hourSessionCols = `s.id::text, s.client_id::text, c.name, s.status, s.pause_requested_at,
	s.created_at, s.updated_at, c.balance_minutes, s.scheduled_end_at, s.scheduled_start_at`
```

Vira:

```go
const hourSessionCols = `s.id::text, s.client_id::text, c.name, s.status, s.pause_requested_at,
	s.end_requested_at, s.created_at, s.updated_at, c.balance_minutes, s.scheduled_end_at, s.scheduled_start_at`
```

`scanHourSession` (linha 169-180) hoje:

```go
func scanHourSession(row pgx.Row) (*HourSession, error) {
	var h HourSession
	err := row.Scan(&h.ID, &h.ClientID, &h.ClientName, &h.Status, &h.PauseRequestedAt,
		&h.CreatedAt, &h.UpdatedAt, &h.BalanceMinutes, &h.ScheduledEndAt, &h.ScheduledStartAt)
```

Vira:

```go
func scanHourSession(row pgx.Row) (*HourSession, error) {
	var h HourSession
	err := row.Scan(&h.ID, &h.ClientID, &h.ClientName, &h.Status, &h.PauseRequestedAt, &h.EndRequestedAt,
		&h.CreatedAt, &h.UpdatedAt, &h.BalanceMinutes, &h.ScheduledEndAt, &h.ScheduledStartAt)
```

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: sem erro.

- [ ] **Step 4: Commit**

```bash
git add apps/api-go/hour_sessions.go
git commit -m "feat(hour): expõe endRequestedAt em HourSession"
```

---

### Task 3: Pedido/recusa de encerramento (espelha pause)

**Files:**
- Modify: `apps/api-go/hour_sessions.go` (store)
- Modify: `apps/api-go/handlers_hour_sessions.go` (handlers)
- Modify: `apps/api-go/routes.go` (rotas)

- [ ] **Step 1: Store — `requestHourSessionEnd` / `denyHourSessionEndRequest`**

Logo depois de `denyHourSessionPauseRequest` (final de `hour_sessions.go`, antes
do comentário `── faturamento avulso ──`), acrescente:

```go
// requestHourSessionEnd marca o pedido de encerramento do cliente (rota
// pública "Já vou embora") — só grava se a sessão ainda estiver rodando
// (active ou paused) e não houver pedido pendente; quem decide encerrar de
// fato é o admin.
func (s *Server) requestHourSessionEnd(ctx context.Context, tokenHash string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_sessions SET end_requested_at = now(), updated_at = now()
		WHERE token_hash = $1 AND status IN ('active', 'paused') AND end_requested_at IS NULL`,
		tokenHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return appErr(http.StatusConflict, "HOUR_SESSION_END_NOT_APPLICABLE",
			"Sessão não está ativa/pausada ou já tem pedido de encerramento pendente")
	}
	return nil
}

// denyHourSessionEndRequest limpa o pedido sem encerrar (admin recusa).
func (s *Server) denyHourSessionEndRequest(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_sessions SET end_requested_at = NULL, updated_at = now()
		WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errHourSessionNotFound
	}
	return nil
}
```

- [ ] **Step 2: Handlers**

Em `handlers_hour_sessions.go`, logo depois de `handleDenyHourSessionPause`
(antes do comentário `── público (sem auth) ──`), acrescente:

```go
// POST /hour-sessions/{id}/deny-end — recusa o pedido de encerramento do cliente
func (s *Server) handleDenyHourSessionEnd(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.denyHourSessionEndRequest(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

E logo depois de `handleRequestHourSessionPause` (final do arquivo, na seção
`── público (sem auth) ──`):

```go
// POST /public/hour-sessions/{token}/request-end
func (s *Server) handleRequestHourSessionEnd(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !isValidHourSessionToken(token) {
		writeErr(w, errHourSessionNotFound)
		return
	}
	if err := s.requestHourSessionEnd(r.Context(), sha256Hex(token)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 3: Resposta pública ganha `endRequested`**

Em `handleGetPublicHourSession` (`handlers_hour_sessions.go`, ~linha 306-332),
o `writeJSON` hoje:

```go
	writeJSON(w, http.StatusOK, map[string]any{
		"clientName": h.ClientName,
		"status":     h.Status,
		"scheduledStartAt": h.ScheduledStartAt,
		"elapsedSeconds":   h.ElapsedSeconds,
		"remainingMinutes": remainingMinutes,
		"pauseRequested":   h.PauseRequestedAt != nil,
	})
```

Vira:

```go
	writeJSON(w, http.StatusOK, map[string]any{
		"clientName": h.ClientName,
		"status":     h.Status,
		"scheduledStartAt": h.ScheduledStartAt,
		"elapsedSeconds":   h.ElapsedSeconds,
		"remainingMinutes": remainingMinutes,
		"pauseRequested":   h.PauseRequestedAt != nil,
		"endRequested":     h.EndRequestedAt != nil,
	})
```

- [ ] **Step 4: Rotas**

Em `routes.go`, logo depois da linha 405 (`deny-pause`):

```go
	mux.HandleFunc("POST /hour-sessions/{id}/deny-pause", s.rateLimit(60, min, s.adminGuard(s.handleDenyHourSessionPause)))
	mux.HandleFunc("POST /hour-sessions/{id}/deny-end", s.rateLimit(60, min, s.adminGuard(s.handleDenyHourSessionEnd)))
```

E logo depois da linha 416 (`request-pause`):

```go
	mux.HandleFunc("POST /public/hour-sessions/{token}/request-pause", s.rateLimit(5, min, s.handleRequestHourSessionPause))
	mux.HandleFunc("POST /public/hour-sessions/{token}/request-end", s.rateLimit(5, min, s.handleRequestHourSessionEnd))
```

- [ ] **Step 5: Gate completo**

Run: `gofmt -l . && go vet ./... && go build ./... && go test ./...`
Expected: tudo verde, `gofmt -l .` sem saída.

- [ ] **Step 6: Commit**

```bash
git add apps/api-go/hour_sessions.go apps/api-go/handlers_hour_sessions.go apps/api-go/routes.go
git commit -m "feat(hour): pedido e recusa de encerramento (\"Já vou embora\")"
```

---

### Task 4: Horário explícito validado em `pause`/`end`

**Files:**
- Modify: `apps/api-go/hour_sessions.go` (validação pura + helper de banco + assinaturas)
- Modify: `apps/api-go/handlers_hour_sessions.go` (resolver o horário no handler)
- Create/Modify: `apps/api-go/hour_sessions_test.go` (testes da validação pura)

- [ ] **Step 1: Escrever os testes que falham primeiro**

Ao final de `hour_sessions_test.go`, acrescente:

```go
// Um horário explícito de pause/end não pode ser no futuro nem anterior ao
// início do trecho em andamento (geraria duração negativa nesse segmento).
func TestValidateEventAtRecusaFuturo(t *testing.T) {
	now := time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	if err := validateEventAt(now.Add(time.Minute), since, now); err == nil {
		t.Fatal("deveria recusar horário no futuro")
	}
}

func TestValidateEventAtRecusaAntesDoInicioDoTrecho(t *testing.T) {
	now := time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	if err := validateEventAt(since.Add(-time.Minute), since, now); err == nil {
		t.Fatal("deveria recusar horário anterior ao início do trecho em andamento")
	}
}

func TestValidateEventAtAceitaDentroDoIntervalo(t *testing.T) {
	now := time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	if err := validateEventAt(since.Add(10*time.Minute), since, now); err != nil {
		t.Fatalf("deveria aceitar horário dentro do intervalo, erro: %v", err)
	}
	// Limites inclusivos.
	if err := validateEventAt(since, since, now); err != nil {
		t.Fatalf("horário igual ao início do trecho deveria ser aceito: %v", err)
	}
	if err := validateEventAt(now, since, now); err != nil {
		t.Fatalf("horário igual a agora deveria ser aceito: %v", err)
	}
}
```

- [ ] **Step 2: Rodar e confirmar que falha (nada disso existe ainda)**

Run: `go test ./... -run TestValidateEventAt -v`
Expected: FAIL — erro de compilação (`validateEventAt` não existe).

- [ ] **Step 3: Validação pura + helper de banco, em `hour_sessions.go`**

Logo depois de `errHourSessionNotFound` (linha 71-72), acrescente:

```go
var errEventAtInFuture = appErr(http.StatusBadRequest, "EVENT_AT_IN_FUTURE",
	"Horário não pode ser no futuro")

// errEventAtBeforeSegmentStart: mensagem inclui o horário real de início pra
// o admin entender por que foi recusado (dígito errado ao digitar, geralmente).
func errEventAtBeforeSegmentStart(since time.Time) error {
	return appErr(http.StatusBadRequest, "EVENT_AT_BEFORE_SEGMENT_START",
		"Horário inválido: a sessão só está rodando desde "+since.Format("15:04"))
}

// validateEventAt é a parte pura (testável sem banco) da validação de um
// horário explícito de pause/end: não pode ser no futuro, nem anterior ao
// início do trecho em andamento (`since`) — geraria duração negativa nesse
// segmento. Limites inclusivos.
func validateEventAt(at, since, now time.Time) error {
	if at.After(now) {
		return errEventAtInFuture
	}
	if at.Before(since) {
		return errEventAtBeforeSegmentStart(since)
	}
	return nil
}
```

Logo depois de `denyHourSessionEndRequest` (Task 3), acrescente:

```go
// hourSessionCurrentSegmentStart devolve o início do trecho em andamento (o
// último 'start'/'resume') — usado por validateEventAt pra recusar um
// horário explícito de pause/end anterior a isso. COALESCE com now() cobre o
// caso sem start/resume ainda (sessão 'scheduled'): pause/end nem deveriam
// ser chamados nesse estado, mas rejeitar aqui é mais seguro que aceitar.
func (s *Server) hourSessionCurrentSegmentStart(ctx context.Context, sessionID string) (time.Time, error) {
	var since time.Time
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(MAX(created_at), now())
		FROM hour_session_events
		WHERE session_id = $1::uuid AND event_type IN ('start', 'resume')`, sessionID).
		Scan(&since)
	return since, err
}
```

- [ ] **Step 4: Rodar os testes do Step 1 — devem passar agora**

Run: `go build ./... && go test ./... -run TestValidateEventAt -v`
Expected: PASS nos 3 casos.

- [ ] **Step 5: `transitionHourSession` e `endHourSession` recebem `eventAt`**

`transitionHourSession` (linha 640): assinatura ganha um parâmetro; o INSERT
do evento passa a gravar `created_at` explícito.

De:

```go
func (s *Server) transitionHourSession(ctx context.Context, id string, actorID int64, from, to, eventType string) (*HourSession, error) {
```

Para:

```go
func (s *Server) transitionHourSession(ctx context.Context, id string, actorID int64, from, to, eventType string, eventAt time.Time) (*HourSession, error) {
```

E o INSERT (linhas 663-667), de:

```go
	if _, err := tx.Exec(ctx, `
		INSERT INTO hour_session_events (session_id, event_type, actor_user_id)
		VALUES ($1::uuid, $2, $3)`,
		id, eventType, actorID); err != nil {
		return nil, err
	}
```

Para:

```go
	if _, err := tx.Exec(ctx, `
		INSERT INTO hour_session_events (session_id, event_type, actor_user_id, created_at)
		VALUES ($1::uuid, $2, $3, $4)`,
		id, eventType, actorID, eventAt); err != nil {
		return nil, err
	}
```

`endHourSession` (linha 690): assinatura ganha o mesmo parâmetro, usado tanto
no cálculo do elapsed (o corte precisa ser no horário escolhido, não em
"agora") quanto no INSERT do evento.

De:

```go
func (s *Server) endHourSession(ctx context.Context, id string, actorID int64) (*HourSession, error) {
```

Para:

```go
func (s *Server) endHourSession(ctx context.Context, id string, actorID int64, eventAt time.Time) (*HourSession, error) {
```

Linha 708 (cálculo do elapsed), de `s.hourSessionElapsedSeconds(ctx, id, time.Now())`
para `s.hourSessionElapsedSeconds(ctx, id, eventAt)`.

O `UPDATE` de encerramento (linhas 727-733), de:

```go
	if _, err := tx.Exec(ctx, `
		UPDATE hour_sessions
		SET status = 'ended', short_code = NULL, short_code_expires_at = NULL,
		    billable_minutes = $2, updated_at = now()
		WHERE id = $1::uuid`,
		id, billableMinutes); err != nil {
		return nil, err
	}
```

Para (aproveita pra limpar um pedido de encerramento pendente, já que a
sessão vai deixar de existir como "algo a decidir"):

```go
	if _, err := tx.Exec(ctx, `
		UPDATE hour_sessions
		SET status = 'ended', short_code = NULL, short_code_expires_at = NULL,
		    billable_minutes = $2, end_requested_at = NULL, updated_at = now()
		WHERE id = $1::uuid`,
		id, billableMinutes); err != nil {
		return nil, err
	}
```

E o INSERT do evento 'end' (linhas 735-739), de:

```go
	if _, err := tx.Exec(ctx, `
		INSERT INTO hour_session_events (session_id, event_type, actor_user_id)
		VALUES ($1::uuid, 'end', $2)`,
		id, actorID); err != nil {
		return nil, err
	}
```

Para:

```go
	if _, err := tx.Exec(ctx, `
		INSERT INTO hour_session_events (session_id, event_type, actor_user_id, created_at)
		VALUES ($1::uuid, 'end', $2, $3)`,
		id, actorID, eventAt); err != nil {
		return nil, err
	}
```

- [ ] **Step 6: Ajustar as 3 chamadas existentes (build vai quebrar até isso)**

`autoEndIfDue` (hour_sessions.go, ~linha 495): `s.endHourSession(ctx, h.ID, createdBy)`
vira `s.endHourSession(ctx, h.ID, createdBy, time.Now())` — encerramento automático
por horário agendado continua usando "agora" (não foi pedido tratamento
retroativo aqui; fica registrado como fora de escopo).

`handlers_hour_sessions.go`:
- `handleResumeHourSession` (linha 266): `s.transitionHourSession(r.Context(), id, userIDFrom(r), "paused", "active", "resume")`
  vira `s.transitionHourSession(r.Context(), id, userIDFrom(r), "paused", "active", "resume", time.Now())`
  — resume não ganha horário customizado nesta entrega (não foi pedido).
- `handlePauseHourSession` e `handleEndHourSession` são reescritos no Step 7
  abaixo (chamam o helper novo em vez de `time.Now()` fixo).

- [ ] **Step 7: Handler — resolver o horário explícito**

Em `handlers_hour_sessions.go`, o import hoje é:

```go
import (
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)
```

Vira (usados pelo helper novo abaixo):

```go
import (
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)
```

Logo antes de `handlePauseHourSession`, acrescente:

```go
// resolveExplicitEventAt lê um horário opcional do corpo ({"at": "..."}) de
// pause/end. Corpo vazio (comportamento de sempre) devolve now(). Presente,
// valida contra o servidor via validateEventAt: não pode ser no futuro nem
// anterior ao início do trecho em andamento da sessão.
func (s *Server) resolveExplicitEventAt(w http.ResponseWriter, r *http.Request, sessionID string) (time.Time, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		At *time.Time `json:"at"`
	}
	if err := decodeJSON(r, &in); err != nil && !errors.Is(err, io.EOF) {
		return time.Time{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido")
	}
	now := time.Now()
	if in.At == nil {
		return now, nil
	}
	since, err := s.hourSessionCurrentSegmentStart(r.Context(), sessionID)
	if err != nil {
		return time.Time{}, err
	}
	if err := validateEventAt(*in.At, since, now); err != nil {
		return time.Time{}, err
	}
	return *in.At, nil
}
```

Reescreva `handlePauseHourSession` e `handleEndHourSession`:

```go
// POST /hour-sessions/{id}/pause — {at?}: horário explícito opcional (ver
// resolveExplicitEventAt). Sem "at", pausa "agora" (comportamento de sempre).
func (s *Server) handlePauseHourSession(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	eventAt, err := s.resolveExplicitEventAt(w, r, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	h, err := s.transitionHourSession(r.Context(), id, userIDFrom(r), "active", "paused", "pause", eventAt)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Pausa manual do admin também limpa um eventual pedido de pausa pendente.
	_ = s.denyHourSessionPauseRequest(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"session": h})
}
```

```go
// POST /hour-sessions/{id}/end — {at?}: mesmo horário explícito opcional de
// pause. Debita o saldo pelo tempo decorrido até esse horário.
func (s *Server) handleEndHourSession(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errHourSessionNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	eventAt, err := s.resolveExplicitEventAt(w, r, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	h, err := s.endHourSession(r.Context(), id, userIDFrom(r), eventAt)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": h})
}
```

- [ ] **Step 8: Gate completo**

Run: `gofmt -l . && go vet ./... && go build ./... && go test ./...`
Expected: tudo verde, `gofmt -l .` sem saída, todos os testes (novos e
existentes, incluindo os de `computeElapsedSeconds`) passando.

- [ ] **Step 9: Commit**

```bash
git add apps/api-go/hour_sessions.go apps/api-go/handlers_hour_sessions.go apps/api-go/hour_sessions_test.go
git commit -m "feat(hour): pause/end aceitam horário explícito validado"
```

---

### Task 5: Deploy e confirmação

- [ ] **Step 1: Push e PR**

```bash
git push -u origin claude-henrique/horas-pausa-encerramento-horario-escolhido
gh pr create --title "feat(hour): escolher horário real ao pausar/encerrar sessão" --body "$(cat <<'EOF'
## Summary
- Corrige o bug relatado: aceitar uma pausa pedida pelo cliente cobrava até o
  horário do CLIQUE DO ADMIN, não o do pedido do cliente.
- pause/end passam a aceitar um horário explícito (`{"at": "..."}`), validado
  contra o servidor (não pode ser no futuro nem anterior ao início do trecho
  em andamento da sessão).
- Novo pedido do cliente "Já vou embora" (end_requested_at), espelha "Pedir
  pausa" — pode ser recusado (deny-end), igual pause.
- Front-end (dashboard, PR próprio) decide o que mandar em `at`: horário do
  pedido, "agora" (omite o campo), ou horário customizado.
- Back-end da spec dashboard/docs/superpowers/specs/2026-09-15-horas-pausa-usa-horario-solicitacao-design.md.

## Test plan
- [x] gofmt -l . && go vet ./... && go build ./... && go test ./...
EOF
)"
```

- [ ] **Step 2: Checar CI**

Run: `gh pr checks <número>`
Expected: `build (api-go)` verde. Se `build (secrets-go)` falhar, confirme com
`gh pr diff <número> --name-only` que o PR não toca `apps/secrets-go` antes de
considerar não-bloqueante.

- [ ] **Step 3: NÃO mesclar sozinho**

Reportar o PR pronto — merge é decisão do Henrique (schema novo em produção,
mesma regra das entregas anteriores desse domínio). Depois do merge + deploy
(Coolify, automático por `watch_paths` em `apps/api-go`), confirmar com:

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X POST https://api.santos-tech.com/hour-sessions/00000000-0000-0000-0000-000000000000/deny-end
```

Expected: `401` (rota existe, exige auth).
