# Professor e horário editável por aula (back-end) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Adicionar `class_session.teacher_id` (override opcional de professor por
aula) e o endpoint `PATCH /portal/sessions/{sessionId}` pra editar data, horário e
professor de uma aula já gerada — back-end da spec do repo `dashboard`
(`docs/superpowers/specs/2026-09-09-aluno-particular-professor-horario-por-aula-design.md`).

**Architecture:** Estende `portal_chamada.go`/`handlers_portal_chamada.go`
(mesmo domínio de `class_session` já usado pra geração de aulas e presença) — não
cria arquivo novo. A leitura (`portalListSessions`) ganha o professor resolvido
com fallback pro professor fixo da turma; a escrita é um endpoint novo (não reusa
`PUT .../attendance`, que é escopo de presença por aluno, não da aula em si).

**Tech Stack:** Go 1.25, `pgx/v5`, `net/http` stdlib, testes `net/http/httptest`
("before DB" — ver `server_test.go`/`handlers_boards_test.go`).

---

## Pré-requisito

Branch já criada a partir do `master` atualizado (pós-merge do PR #346):
`claude-henrique/aluno-particular-professor-horario-aula`, em
`C:\Users\55169\Documents\GitHub\santos-tech-infra`. Rode os comandos a partir
desse diretório.

---

### Task 1: Migration — `class_session.teacher_id`

**Files:**
- Modify: `apps/api-go/portal_migrate.go:111-112` (final da const `portalMigration`)

- [ ] **Step 1: Acrescentar o `ALTER TABLE` ao final da const `portalMigration`**

Em `apps/api-go/portal_migrate.go`, a const termina assim hoje (linhas 108-112):

```go
-- sobretudo pra matrícula particular — enrollment.individual=true). NULL = não
-- preenchido, turma de grupo normalmente fica assim; o front usa TotalPhases (o
-- currículo do curso) como denominador do progresso nesse caso.
ALTER TABLE enrollment ADD COLUMN IF NOT EXISTS contracted_lessons INTEGER;
`
```

Substitua por:

```go
-- sobretudo pra matrícula particular — enrollment.individual=true). NULL = não
-- preenchido, turma de grupo normalmente fica assim; o front usa TotalPhases (o
-- currículo do curso) como denominador do progresso nesse caso.
ALTER TABLE enrollment ADD COLUMN IF NOT EXISTS contracted_lessons INTEGER;

-- class_session.teacher_id: override do professor pra UMA aula específica —
-- nulo (padrão) usa o professor fixo da turma (class_teacher), como sempre foi.
-- Existe pra cobrir reposição/troca pontual (ex.: professor da aula 8 diferente
-- do professor fixo da turma), não pra ser preenchido em toda aula.
ALTER TABLE class_session ADD COLUMN IF NOT EXISTS teacher_id INTEGER;
`
```

- [ ] **Step 2: Build**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro. (Igual à entrega anterior, não há Postgres local nesta
máquina pra testar o boot da migration de fato — idempotente, roda no próximo
deploy real.)

- [ ] **Step 3: Commit**

```bash
git add apps/api-go/portal_migrate.go
git commit -m "feat(portal): adiciona class_session.teacher_id"
```

---

### Task 2: `PATCH /portal/sessions/{sessionId}` — editar data/horário/professor

**Files:**
- Modify: `apps/api-go/portal_chamada.go` (struct de input + validação + store `portalUpdateSession`)
- Modify: `apps/api-go/handlers_portal_chamada.go` (novo handler)
- Modify: `apps/api-go/portal_routes.go:65` (registro da rota)
- Test: `apps/api-go/handlers_portal_test.go`

- [ ] **Step 1: Escrever os testes que falham (validação de formato e de intervalo)**

Acrescente ao final de `apps/api-go/handlers_portal_test.go`:

```go
func TestPortalUpdateSessionValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})

	cases := []struct {
		name string
		body string
	}{
		{"data inválida", `{"date":"09/09/2026"}`},
		{"startTime inválido", `{"startTime":"19h30"}`},
		{"endTime inválido", `{"endTime":"vinte e uma"}`},
		{"fim antes do início", `{"startTime":"21:00","endTime":"19:30"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("PATCH", "/portal/sessions/1", strings.NewReader(tc.body))
			r.SetPathValue("sessionId", "1")
			w := httptest.NewRecorder()
			s.handlePortalUpdateSession(w, reqAs(r, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s: code=%d want %d", tc.name, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestPortalUpdateSessionBadIDBeforeDB(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("PATCH", "/portal/sessions/x", strings.NewReader(`{"date":"2026-09-10"}`))
	r.SetPathValue("sessionId", "x")
	w := httptest.NewRecorder()
	s.handlePortalUpdateSession(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want %d", w.Code, http.StatusBadRequest)
	}
}
```

- [ ] **Step 2: Rodar e confirmar que falha (nada disso existe ainda)**

Run: `go test ./... -run TestPortalUpdateSession -v`
Expected: FAIL — erro de compilação (`handlePortalUpdateSession` não existe).

- [ ] **Step 3: Struct de input + validação, em `portal_chamada.go`**

Logo depois do struct `portalMySessionDTO` (linha 47), acrescente:

```go
// portalSessionUpdateInput é o corpo do PATCH que edita uma aula já gerada —
// update parcial: só os campos presentes no payload mudam. Sem suporte a
// "limpar" teacherId com null explícito (mesma limitação de *int do PATCH de
// matrícula — ver spec) — só dá pra trocar por outro professor.
type portalSessionUpdateInput struct {
	Date      *string `json:"date,omitempty"`
	StartTime *string `json:"startTime,omitempty"`
	EndTime   *string `json:"endTime,omitempty"`
	TeacherID *int64  `json:"teacherId,omitempty"`
}

func (in portalSessionUpdateInput) validate() error {
	if in.Date != nil {
		if _, err := time.Parse("2006-01-02", *in.Date); err != nil {
			return validationErr("date inválida (use YYYY-MM-DD)")
		}
	}
	if in.StartTime != nil {
		if _, err := time.Parse("15:04", *in.StartTime); err != nil {
			return validationErr("startTime inválido (use HH:MM)")
		}
	}
	if in.EndTime != nil {
		if _, err := time.Parse("15:04", *in.EndTime); err != nil {
			return validationErr("endTime inválido (use HH:MM)")
		}
	}
	if in.StartTime != nil && in.EndTime != nil && *in.EndTime <= *in.StartTime {
		return validationErr("o horário de fim precisa ser depois do início")
	}
	return nil
}
```

- [ ] **Step 4: Store `portalUpdateSession`, ao final de `portal_chamada.go`**

```go
// portalUpdateSession edita uma aula já gerada — update parcial, só os campos
// presentes em `in` entram no SET. Colisão com outra aula da mesma turma (UNIQUE
// class_id+date+start_time) volta como 409 amigável via portalDBErr, sem
// tratamento especial aqui.
func (s *Server) portalUpdateSession(ctx context.Context, sessionID int64, in portalSessionUpdateInput) error {
	sets := []string{}
	args := []any{}
	n := 1
	if in.Date != nil {
		sets = append(sets, fmt.Sprintf("date=$%d", n))
		args = append(args, *in.Date)
		n++
	}
	if in.StartTime != nil {
		sets = append(sets, fmt.Sprintf("start_time=$%d::time", n))
		args = append(args, *in.StartTime)
		n++
	}
	if in.EndTime != nil {
		sets = append(sets, fmt.Sprintf("end_time=$%d::time", n))
		args = append(args, *in.EndTime)
		n++
	}
	if in.TeacherID != nil {
		sets = append(sets, fmt.Sprintf("teacher_id=$%d", n))
		args = append(args, *in.TeacherID)
		n++
	}
	if len(sets) == 0 {
		return validationErr("nada pra atualizar")
	}
	sets = append(sets, "updated_at=NOW()")
	args = append(args, sessionID)
	query := fmt.Sprintf("UPDATE class_session SET %s WHERE id=$%d", strings.Join(sets, ", "), n)
	tag, err := s.portalDB.Exec(ctx, query, args...)
	if err != nil {
		return portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Aula")
	}
	s.invalidatePortalOverview()
	return nil
}
```

O bloco `import` de `portal_chamada.go` hoje é:

```go
import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)
```

`strings` já está lá; `fmt` **não está** — adicione (ordem alfabética, junto dos
outros stdlib):

```go
import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)
```

- [ ] **Step 5: Handler, em `handlers_portal_chamada.go`**

O bloco `import` deste arquivo hoje é só `"context"`, `"net/http"`, `"time"` — sem
`"fmt"`. Adicione (usado no `portalLogActivity` abaixo):

```go
import (
	"context"
	"fmt"
	"net/http"
	"time"
)
```

Logo depois de `handlePortalListSessions` (linha 78 do arquivo atual), acrescente:

```go
// handlePortalUpdateSession (PATCH /portal/sessions/{sessionId}) — edita
// data/horário/professor de uma aula já gerada. Update parcial: campo ausente
// no payload não muda.
func (s *Server) handlePortalUpdateSession(w http.ResponseWriter, r *http.Request) {
	sessionID, err := portalPathID(r, "sessionId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalSessionUpdateInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalUpdateSession(r.Context(), sessionID, in); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "session_update", "session", fmt.Sprint(sessionID), map[string]any{
		"date": in.Date, "startTime": in.StartTime, "endTime": in.EndTime, "teacherId": in.TeacherID,
	})
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 6: Registrar a rota**

Em `apps/api-go/portal_routes.go`, logo depois da linha 65
(`PUT /portal/sessions/{sessionId}/attendance`):

```go
	mux.HandleFunc("PUT /portal/sessions/{sessionId}/attendance", s.rateLimit(120, min, s.portalWrite("portal_turmas", s.handlePortalSetAttendance)))
	mux.HandleFunc("PATCH /portal/sessions/{sessionId}", s.rateLimit(60, min, s.portalWrite("portal_turmas", s.handlePortalUpdateSession)))
```

- [ ] **Step 7: Rodar os testes do Step 1 — devem passar agora**

Run: `go build ./... && go test ./... -run TestPortalUpdateSession -v`
Expected: PASS nos 5 casos (4 de validação + bad ID).

- [ ] **Step 8: Gate completo**

Run: `gofmt -l . && go vet ./... && go build ./... && go test ./...`
Expected: `gofmt -l .` sem saída; tudo verde.

- [ ] **Step 9: Commit**

```bash
git add apps/api-go/portal_chamada.go apps/api-go/handlers_portal_chamada.go apps/api-go/portal_routes.go apps/api-go/handlers_portal_test.go
git commit -m "feat(portal): PATCH /portal/sessions/{id} — edita data, horário e professor da aula"
```

---

### Task 3: Professor resolvido (com fallback) na leitura de aulas

**Files:**
- Modify: `apps/api-go/portal_chamada.go:17-26` (`portalSessionDTO`), `:93-113` (`portalListSessions`)

- [ ] **Step 1: Campos novos no DTO**

Substitua `portalSessionDTO` (linhas 17-26):

```go
type portalSessionDTO struct {
	ID        string                `json:"id"`
	ClassID   string                `json:"classId"`
	Date      string                `json:"date"` // AAAA-MM-DD
	StartTime string                `json:"startTime,omitempty"`
	EndTime   string                `json:"endTime,omitempty"`
	Canceled  bool                  `json:"canceled"`
	Note      string                `json:"note,omitempty"`
	Presencas []portalAttendanceDTO `json:"presencas"`
}
```

por:

```go
type portalSessionDTO struct {
	ID        string                `json:"id"`
	ClassID   string                `json:"classId"`
	Date      string                `json:"date"` // AAAA-MM-DD
	StartTime string                `json:"startTime,omitempty"`
	EndTime   string                `json:"endTime,omitempty"`
	Canceled  bool                  `json:"canceled"`
	Note      string                `json:"note,omitempty"`
	// TeacherID: professor específico desta aula (nulo = usa o fixo da turma).
	// TeacherName: já resolvido (o da aula se setado, senão o(s) fixo(s) da
	// turma) — o front não precisa saber calcular o fallback.
	TeacherID   *string               `json:"teacherId"`
	TeacherName *string               `json:"teacherName"`
	Presencas   []portalAttendanceDTO `json:"presencas"`
}
```

- [ ] **Step 2: Resolver o professor na query de `portalListSessions`**

Substitua a primeira query de `portalListSessions` (linhas 94-98):

```go
	rows, err := s.portalDB.Query(ctx,
		`SELECT id::text, class_id::text, to_char(date,'YYYY-MM-DD'),
		        COALESCE(to_char(start_time,'HH24:MI'),''), COALESCE(to_char(end_time,'HH24:MI'),''),
		        canceled, COALESCE(note,'')
		 FROM class_session WHERE class_id=$1 ORDER BY date DESC, start_time`, classID)
```

por:

```go
	rows, err := s.portalDB.Query(ctx,
		`SELECT cs.id::text, cs.class_id::text, to_char(cs.date,'YYYY-MM-DD'),
		        COALESCE(to_char(cs.start_time,'HH24:MI'),''), COALESCE(to_char(cs.end_time,'HH24:MI'),''),
		        cs.canceled, COALESCE(cs.note,''), cs.teacher_id::text,
		        COALESCE(
		          (SELECT name FROM "user" WHERE id = cs.teacher_id),
		          (SELECT string_agg(t.name, ', ' ORDER BY t.name) FROM class_teacher ct
		             JOIN "user" t ON t.id = ct.user_id WHERE ct.class_id = cs.class_id)
		        )
		 FROM class_session cs WHERE cs.class_id=$1 ORDER BY cs.date DESC, cs.start_time`, classID)
```

E o `Scan` logo abaixo (linha 107):

```go
		if err := rows.Scan(&d.ID, &d.ClassID, &d.Date, &d.StartTime, &d.EndTime, &d.Canceled, &d.Note); err != nil {
```

por:

```go
		if err := rows.Scan(&d.ID, &d.ClassID, &d.Date, &d.StartTime, &d.EndTime, &d.Canceled, &d.Note, &d.TeacherID, &d.TeacherName); err != nil {
```

- [ ] **Step 3: Gate completo**

Run: `gofmt -l . && go vet ./... && go build ./... && go test ./...`
Expected: tudo verde. (Sem teste de DB pro SELECT novo — mesmo motivo das
entregas anteriores, precisa de Postgres real.)

- [ ] **Step 4: Commit**

```bash
git add apps/api-go/portal_chamada.go
git commit -m "feat(portal): resolve professor por aula (com fallback pro fixo da turma)"
```

---

### Task 4: Deploy e confirmação

- [ ] **Step 1: Push e PR**

```bash
git push -u origin claude-henrique/aluno-particular-professor-horario-aula
gh pr create --title "feat(portal): professor e horário editável por aula" --body "Back-end da spec dashboard/docs/superpowers/specs/2026-09-09-aluno-particular-professor-horario-por-aula-design.md: class_session.teacher_id, PATCH /portal/sessions/{id}, e professor resolvido (com fallback) na leitura de aulas."
```

- [ ] **Step 2: Checar CI**

Run: `gh pr checks <número>`
Expected: `build (api-go)` verde nas duas variantes de matriz. Se `build
(secrets-go)` falhar, confirme com `gh pr diff <número> --name-only` que o PR não
toca `apps/secrets-go` antes de considerar não-bloqueante (mesmo padrão do PR
#346).

- [ ] **Step 3: NÃO mesclar sozinho**

Reportar o PR pronto — merge é decisão do Henrique (schema novo em produção).
Depois do merge + deploy (Coolify, automático por `watch_paths` em
`apps/api-go`), confirmar com:

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X PATCH https://api.santos-tech.com/portal/sessions/1
```

Expected: `401` (rota existe, exige auth).
