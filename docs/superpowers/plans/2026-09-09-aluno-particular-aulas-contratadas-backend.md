# Aulas contratadas + histórico do aluno (back-end) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Adicionar o campo `contracted_lessons` na matrícula (`enrollment`) e um
endpoint self-service `GET /portal/me/sessions` pro aluno ver o histórico de aulas —
back-end da spec do repo `dashboard`
(`docs/superpowers/specs/2026-09-09-aluno-particular-aulas-contratadas-historico-design.md`).

**Architecture:** Estende endpoints/queries já existentes do domínio portal
(`apps/api-go/portal_*.go`) em vez de criar rotas paralelas — mesmo PATCH que já marca
"particular" ganha o campo novo, mesmas queries de overview ganham a coluna. O único
endpoint novo é autosserviço puro (`authGuard`, sem permissão de portal), seguindo o
padrão já estabelecido por `GET /portal/me/overview`.

**Tech Stack:** Go 1.25, `pgx/v5` (SQL cru, sem ORM), `net/http` stdlib (mux com
padrões `"MÉTODO /rota"`), testes com `net/http/httptest` (sem banco real — ver
`testServer()`/`reqAs()` em `server_test.go`/`handlers_boards_test.go`).

---

## Pré-requisito

Este plano assume o worktree do `dashboard` já resolvido (branch
`claude-henrique/private-student-ui-8a5810` recriada sobre o `origin/main` real —
feito em 09/09). Rode a partir de `C:\Users\55169\Documents\GitHub\santos-tech-infra`,
branch nova a partir do `main`/`master` atual desse repo:

```bash
git checkout master && git pull origin master
git checkout -b claude-henrique/aluno-particular-aulas-contratadas
```

---

### Task 1: Migration — `enrollment.contracted_lessons`

**Files:**
- Modify: `apps/api-go/portal_migrate.go:95-106` (final do bloco `attendance`, dentro
  da const `portalMigration`)

- [ ] **Step 1: Acrescentar o `ALTER TABLE` ao final da const `portalMigration`**

Em `apps/api-go/portal_migrate.go`, logo depois do índice
`idx_attendance_user` (linha 105) e antes do fechamento da crase da const (linha 106),
acrescente:

```go
CREATE INDEX IF NOT EXISTS idx_attendance_user ON attendance(user_id);

-- enrollment.contracted_lessons: pacote de aulas contratado pelo aluno (relevante
-- sobretudo pra matrícula particular — enrollment.individual=true). NULL = não
-- preenchido, turma de grupo normalmente fica assim; o front usa TotalPhases (o
-- currículo do curso) como denominador do progresso nesse caso.
ALTER TABLE enrollment ADD COLUMN IF NOT EXISTS contracted_lessons INTEGER;
`
```

(A crase de fechamento `` ` `` que já existia continua no lugar — só o `ALTER TABLE`
entra antes dela.)

- [ ] **Step 2: Confirmar que compila e o boot roda a migration sem erro**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro.

Run (com `DATABASE_URL`/`.env` local configurado, se disponível):
`go run . 2>&1 | head -30`
Expected: log de boot sem erro relacionado a `portal_migrate`/`ALTER TABLE`. Se não
houver banco local disponível nesta máquina, pule este sub-passo — a migration é
idempotente e vai rodar no primeiro boot em qualquer ambiente com o banco certo (dev
ou produção), sem exigir passo manual.

- [ ] **Step 3: Commit**

```bash
git add apps/api-go/portal_migrate.go
git commit -m "feat(portal): adiciona enrollment.contracted_lessons"
```

---

### Task 2: PATCH da matrícula aceita `contractedLessons`

**Files:**
- Modify: `apps/api-go/portal_models.go:274-278` (struct `portalStudentIndividualInput`)
- Modify: `apps/api-go/handlers_portal_classes.go:190-216` (`handlePortalSetStudentIndividual`)
- Modify: `apps/api-go/portal_store.go:597-607` (`portalSetStudentIndividual`)
- Test: `apps/api-go/handlers_portal_test.go`

- [ ] **Step 1: Escrever o teste de validação que falha (contractedLessons negativo)**

Acrescente ao final de `apps/api-go/handlers_portal_test.go`:

```go
func TestPortalSetStudentIndividualValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})

	neg := -1
	r := httptest.NewRequest("PATCH", "/portal/classes/1/students/1", strings.NewReader(`{"individual":true,"contractedLessons":-1}`))
	r.SetPathValue("classId", "1")
	r.SetPathValue("studentId", "1")
	w := httptest.NewRecorder()
	s.handlePortalSetStudentIndividual(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("contractedLessons negativo: code=%d want %d", w.Code, http.StatusBadRequest)
	}
	_ = neg
}
```

- [ ] **Step 2: Rodar e confirmar que falha (a struct ainda não tem o campo)**

Run: `cd apps/api-go && go test ./... -run TestPortalSetStudentIndividualValidationBeforeDB -v`
Expected: FAIL — erro de compilação (`unknown field ContractedLessons` não existe
ainda; o teste em si nem compila, o que conta como "falhou" pra este passo do TDD).

- [ ] **Step 3: Estender `portalStudentIndividualInput` com validação**

Em `apps/api-go/portal_models.go`, substitua (linhas 274-278):

```go
// portalStudentIndividualInput é o corpo do PATCH que marca/desmarca uma
// matrícula como particular.
type portalStudentIndividualInput struct {
	Individual bool `json:"individual"`
}
```

por:

```go
// portalStudentIndividualInput é o corpo do PATCH que atualiza uma matrícula:
// marca/desmarca como particular e/ou define o pacote de aulas contratadas.
// Individual é sempre obrigatório no payload (todo PATCH reenvia o valor atual,
// mesmo quando só ContractedLessons mudou) — o zero-value de bool ausente no
// JSON é `false`, que resetaria sem querer o toggle "particular" se fosse opcional.
type portalStudentIndividualInput struct {
	Individual bool `json:"individual"`
	// ContractedLessons: pacote de aulas contratado (relevante sobretudo pra
	// matrícula particular). nil = não veio no payload, mantém o valor salvo —
	// não dá pra "limpar" com null explícito nesse desenho simples (ver spec).
	ContractedLessons *int `json:"contractedLessons,omitempty"`
}

func (in portalStudentIndividualInput) validate() error {
	if in.ContractedLessons != nil && *in.ContractedLessons < 1 {
		return validationErr("contractedLessons deve ser maior que zero")
	}
	return nil
}
```

- [ ] **Step 4: Chamar a validação e passar o campo novo no handler**

Em `apps/api-go/handlers_portal_classes.go`, substitua o corpo de
`handlePortalSetStudentIndividual` (linhas 205-215):

```go
	var in portalStudentIndividualInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := s.portalSetStudentIndividual(r.Context(), classID, studentID, in.Individual); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "class_student_individual", "class", fmt.Sprint(classID), map[string]any{"studentId": fmt.Sprint(studentID), "individual": in.Individual})
	w.WriteHeader(http.StatusNoContent)
}
```

por:

```go
	var in portalStudentIndividualInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalSetStudentIndividual(r.Context(), classID, studentID, in.Individual, in.ContractedLessons); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "class_student_individual", "class", fmt.Sprint(classID), map[string]any{
		"studentId": fmt.Sprint(studentID), "individual": in.Individual, "contractedLessons": in.ContractedLessons,
	})
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 5: Estender a store com update parcial**

Em `apps/api-go/portal_store.go`, substitua `portalSetStudentIndividual` (linhas
597-607):

```go
func (s *Server) portalSetStudentIndividual(ctx context.Context, classID, studentID int64, individual bool) error {
	tag, err := s.portalDB.Exec(ctx, `UPDATE enrollment SET individual=$3 WHERE class_id=$1 AND user_id=$2`, classID, studentID, individual)
	if err != nil {
		return portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Matrícula")
	}
	s.invalidatePortalOverview()
	return nil
}
```

por:

```go
// portalSetStudentIndividual atualiza o toggle "particular" e, opcionalmente, o
// pacote de aulas contratadas de uma matrícula. contractedLessons só entra na
// cláusula SET quando veio no payload (ponteiro não-nil) — update parcial.
func (s *Server) portalSetStudentIndividual(ctx context.Context, classID, studentID int64, individual bool, contractedLessons *int) error {
	query := `UPDATE enrollment SET individual=$3 WHERE class_id=$1 AND user_id=$2`
	args := []any{classID, studentID, individual}
	if contractedLessons != nil {
		query = `UPDATE enrollment SET individual=$3, contracted_lessons=$4 WHERE class_id=$1 AND user_id=$2`
		args = append(args, *contractedLessons)
	}
	tag, err := s.portalDB.Exec(ctx, query, args...)
	if err != nil {
		return portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Matrícula")
	}
	s.invalidatePortalOverview()
	return nil
}
```

- [ ] **Step 6: Limpar a variável não usada do Step 1 e rodar o teste**

No teste escrito no Step 1, remova a linha `neg := -1` e `_ = neg` (eram só pra
evitar erro de compilação antes da struct existir — agora o literal `-1` já vai
direto no JSON da request, não precisa da variável):

```go
func TestPortalSetStudentIndividualValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})

	r := httptest.NewRequest("PATCH", "/portal/classes/1/students/1", strings.NewReader(`{"individual":true,"contractedLessons":-1}`))
	r.SetPathValue("classId", "1")
	r.SetPathValue("studentId", "1")
	w := httptest.NewRecorder()
	s.handlePortalSetStudentIndividual(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("contractedLessons negativo: code=%d want %d", w.Code, http.StatusBadRequest)
	}
}
```

Run: `go test ./... -run TestPortalSetStudentIndividualValidationBeforeDB -v`
Expected: PASS.

- [ ] **Step 7: Rodar a suíte inteira do pacote (garantir que nada quebrou)**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: `gofmt -l .` sem saída; `go test` todo verde. (Confirmado que
`handlePortalSetStudentIndividual` é o único chamador de `portalSetStudentIndividual`
no repo — `grep -rn "portalSetStudentIndividual("`, 09/09 — então não há outro call
site pra ajustar.)

- [ ] **Step 8: Commit**

```bash
git add apps/api-go/portal_models.go apps/api-go/handlers_portal_classes.go apps/api-go/portal_store.go apps/api-go/handlers_portal_test.go
git commit -m "feat(portal): PATCH de matrícula aceita contractedLessons"
```

---

### Task 3: `contractedLessons` na leitura (overview do aluno + detalhe da turma)

**Files:**
- Modify: `apps/api-go/portal_models.go:222-228` (`portalStudentDTO`)
- Modify: `apps/api-go/portal_models.go:254-272` (`portalStudentOverviewDTO`)
- Modify: `apps/api-go/portal_store.go:570-592` (`portalListClassStudents` — alimenta `GET /portal/classes/{classId}`, consumido pelo admin em `AlunosSection`)
- Modify: `apps/api-go/portal_store.go:616-733` (`portalStudentsOverview`, `portalMyOverview`)

- [ ] **Step 1: Adicionar o campo ao DTO**

Em `apps/api-go/portal_models.go`, no struct `portalStudentOverviewDTO` (linhas
254-272), logo depois do campo `Individual`:

```go
	TeacherName     *string `json:"teacherName"`
	Individual      bool    `json:"individual"`
	// ContractedLessons: pacote de aulas contratado pelo aluno (nil = não
	// preenchido — turma de grupo normalmente fica assim). Quando preenchido,
	// o front (dashboard/web) usa como denominador do progresso em vez de
	// TotalPhases (currículo do curso inteiro).
	ContractedLessons *int `json:"contractedLessons"`
```

- [ ] **Step 2: Incluir a coluna em `portalStudentsOverview`**

Em `apps/api-go/portal_store.go`, dentro de `portalStudentsOverview` (a partir da
linha 630), no `SELECT`, logo depois de `e.individual`:

```go
		       (SELECT string_agg(t.name, ', ' ORDER BY t.name) FROM class_teacher ct
		          JOIN "user" t ON t.id = ct.user_id
		        WHERE ct.class_id = cl.id),
		       e.individual, e.contracted_lessons
```

E no `Scan` correspondente (linha 663), logo depois de `&dto.Individual`:

```go
			&dto.TotalPhases, &dto.CompletedPhases, &dto.AulasDadas, &dto.Faltas, &dto.TeacherName, &dto.Individual, &dto.ContractedLessons); err != nil {
```

- [ ] **Step 3: Mesma mudança em `portalMyOverview`**

Repita o Step 2 na query de `portalMyOverview` (a partir da linha 687): mesmo
`SELECT` (`e.individual, e.contracted_lessons`) e mesmo `Scan`
(`&dto.Individual, &dto.ContractedLessons`).

- [ ] **Step 4: Adicionar o campo a `portalStudentDTO` (detalhe da turma, admin)**

Sem este passo, a tela de admin (`TurmaDetalhe` → `AlunosSection`, ver Task 2 do
plano do front-end) não tem como MOSTRAR o valor já salvo antes de editar — o
campo apareceria sempre vazio mesmo quando já preenchido. Em
`apps/api-go/portal_models.go`, substitua `portalStudentDTO` (linhas 222-228):

```go
type portalStudentDTO struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Role       int16  `json:"role"`
	Individual bool   `json:"individual"`
}
```

por:

```go
type portalStudentDTO struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Role       int16  `json:"role"`
	Individual bool   `json:"individual"`
	// ContractedLessons: mesmo campo de portalStudentOverviewDTO — aqui é o
	// valor ATUAL da matrícula, pro admin ver o que já está salvo antes de
	// editar (ver AlunosSection no dashboard/web).
	ContractedLessons *int `json:"contractedLessons"`
}
```

- [ ] **Step 5: Incluir a coluna em `portalListClassStudents`**

Em `apps/api-go/portal_store.go`, substitua a query e o `Scan` de
`portalListClassStudents` (linhas 575-586):

```go
	rows, err := s.portalDB.Query(ctx, `SELECT u.id::text, COALESCE(u.email,''), COALESCE(u.name,''), u.role, e.individual
		FROM enrollment e JOIN "user" u ON u.id = e.user_id
		WHERE e.class_id=$1 ORDER BY COALESCE(u.name,'') ASC, u.id ASC
		LIMIT $2 OFFSET $3`, classID, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalStudentDTO{}
	for rows.Next() {
		var dto portalStudentDTO
		if err := rows.Scan(&dto.ID, &dto.Email, &dto.Name, &dto.Role, &dto.Individual); err != nil {
			return nil, 0, err
		}
		items = append(items, dto)
	}
```

por:

```go
	rows, err := s.portalDB.Query(ctx, `SELECT u.id::text, COALESCE(u.email,''), COALESCE(u.name,''), u.role, e.individual, e.contracted_lessons
		FROM enrollment e JOIN "user" u ON u.id = e.user_id
		WHERE e.class_id=$1 ORDER BY COALESCE(u.name,'') ASC, u.id ASC
		LIMIT $2 OFFSET $3`, classID, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalStudentDTO{}
	for rows.Next() {
		var dto portalStudentDTO
		if err := rows.Scan(&dto.ID, &dto.Email, &dto.Name, &dto.Role, &dto.Individual, &dto.ContractedLessons); err != nil {
			return nil, 0, err
		}
		items = append(items, dto)
	}
```

- [ ] **Step 6: Build (sem teste de DB — SELECT novo só é exercitável com Postgres real, fora do escopo dos testes "before DB" deste pacote)**

Run: `go build ./... && go vet ./... && gofmt -l .`
Expected: sem erro, `gofmt -l .` sem saída.

- [ ] **Step 7: Commit**

```bash
git add apps/api-go/portal_models.go apps/api-go/portal_store.go
git commit -m "feat(portal): expõe contractedLessons no overview e no detalhe da turma"
```

---

### Task 4: `GET /portal/me/sessions` — histórico self-service

**Files:**
- Modify: `apps/api-go/portal_models.go` (novo helper `portalQueryID`, perto de `portalPathID:58-64`)
- Modify: `apps/api-go/portal_chamada.go` (novo DTO + store `portalMySessions`)
- Modify: `apps/api-go/handlers_portal_chamada.go` (novo handler `handlePortalMySessions`)
- Modify: `apps/api-go/portal_routes.go:21` (registro da rota)
- Test: `apps/api-go/handlers_portal_test.go`

- [ ] **Step 1: Escrever os testes que falham (guard de auth + classId inválido)**

Acrescente ao final de `apps/api-go/handlers_portal_test.go`:

```go
func TestPortalMySessionsRequiresAuthBeforeDB(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.authGuard(s.handlePortalMySessions)(w, httptest.NewRequest("GET", "/portal/me/sessions?classId=1", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestPortalMySessionsBadClassIdBeforeDB(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("GET", "/portal/me/sessions?classId=x", nil)
	w := httptest.NewRecorder()
	s.handlePortalMySessions(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("classId inválido: code=%d want %d", w.Code, http.StatusBadRequest)
	}
}

func TestPortalMySessionsMissingClassIdBeforeDB(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("GET", "/portal/me/sessions", nil)
	w := httptest.NewRecorder()
	s.handlePortalMySessions(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("classId ausente: code=%d want %d", w.Code, http.StatusBadRequest)
	}
}
```

- [ ] **Step 2: Rodar e confirmar que falha (nada disso existe ainda)**

Run: `go test ./... -run TestPortalMySessions -v`
Expected: FAIL — erro de compilação (`handlePortalMySessions` não existe).

- [ ] **Step 3: Helper `portalQueryID` (ler um id de query string, mesmo contrato de `portalPathID`)**

Em `apps/api-go/portal_models.go`, logo depois de `portalPathID` (linha 64), acrescente:

```go
// portalQueryID lê um id inteiro positivo de um parâmetro de query string —
// mesmo contrato de erro que portalPathID (path param), pra rotas tipo
// GET /portal/me/sessions?classId=123 que não têm o id no path.
func portalQueryID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, appErr(http.StatusBadRequest, "VALIDATION_ERROR", name+" inválido")
	}
	return id, nil
}
```

- [ ] **Step 4: DTO + store `portalMySessions`**

Em `apps/api-go/portal_chamada.go`, logo depois do struct `portalAttendanceDTO`
(linha 33), acrescente:

```go
// portalMySessionDTO é uma linha do histórico self-service do próprio aluno —
// mais enxuto que portalSessionDTO (não traz Presencas de outros alunos, só o
// status da PRÓPRIA pessoa logada).
type portalMySessionDTO struct {
	ID        string `json:"id"`
	Date      string `json:"date"`
	StartTime string `json:"startTime,omitempty"`
	EndTime   string `json:"endTime,omitempty"`
	Canceled  bool   `json:"canceled"`
	Status    string `json:"status,omitempty"` // vazio = chamada não feita
	Note      string `json:"note,omitempty"`
}
```

E, ao final do arquivo (depois de `portalGerarAulasDeTodasAsTurmas`), acrescente:

```go
// portalMySessions é o histórico self-service de aulas do PRÓPRIO aluno numa
// turma — diferente de portalListSessions (staff, todos os alunos da turma):
// aqui é só a chamada da pessoa logada, e exige matrícula própria na turma
// (não aceita studentId do cliente — só classId, e valida contra o userID da
// sessão). Sem isso, qualquer usuário logado poderia ler o histórico de
// qualquer turma só sabendo o classId.
func (s *Server) portalMySessions(ctx context.Context, classID, userID int64) ([]portalMySessionDTO, error) {
	var matriculado bool
	if err := s.portalDB.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM enrollment WHERE class_id=$1 AND user_id=$2)`, classID, userID).Scan(&matriculado); err != nil {
		return nil, err
	}
	if !matriculado {
		return nil, appErr(http.StatusForbidden, "NAO_MATRICULADO", "Você não está matriculado nesta turma")
	}
	rows, err := s.portalDB.Query(ctx,
		`SELECT cs.id::text, to_char(cs.date,'YYYY-MM-DD'),
		        COALESCE(to_char(cs.start_time,'HH24:MI'),''), COALESCE(to_char(cs.end_time,'HH24:MI'),''),
		        cs.canceled, COALESCE(a.status,''), COALESCE(a.note,'')
		 FROM class_session cs
		 LEFT JOIN attendance a ON a.session_id = cs.id AND a.user_id = $2
		 WHERE cs.class_id = $1
		 ORDER BY cs.date DESC, cs.start_time DESC`, classID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	itens := []portalMySessionDTO{}
	for rows.Next() {
		var d portalMySessionDTO
		if err := rows.Scan(&d.ID, &d.Date, &d.StartTime, &d.EndTime, &d.Canceled, &d.Status, &d.Note); err != nil {
			return nil, err
		}
		itens = append(itens, d)
	}
	return itens, rows.Err()
}
```

Adicione `"net/http"` aos imports de `apps/api-go/portal_chamada.go` (usado por
`appErr(http.StatusForbidden, ...)` — hoje o arquivo não importa `net/http`).

- [ ] **Step 5: Handler `handlePortalMySessions`**

Em `apps/api-go/handlers_portal_chamada.go`, logo depois de
`handlePortalListSessions` (linha 78), acrescente:

```go
// handlePortalMySessions (GET /portal/me/sessions?classId=) — histórico de
// aulas do próprio aluno logado, autosserviço como GET /portal/me/overview: só
// authGuard, sem permissão de portal — o escopo já é a própria pessoa. Nunca
// aceita studentId do cliente.
func (s *Server) handlePortalMySessions(w http.ResponseWriter, r *http.Request) {
	classID, err := portalQueryID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado"))
		return
	}
	itens, err := s.portalMySessions(r.Context(), classID, u.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": itens})
}
```

- [ ] **Step 6: Registrar a rota**

Em `apps/api-go/portal_routes.go`, logo depois da linha 21
(`GET /portal/me/overview`):

```go
	mux.HandleFunc("GET /portal/me/overview", s.authGuard(s.handlePortalMyOverview))
	mux.HandleFunc("GET /portal/me/sessions", s.authGuard(s.handlePortalMySessions))
```

- [ ] **Step 7: Rodar os testes do Step 1 — devem passar agora**

Run: `go build ./... && go test ./... -run TestPortalMySessions -v`
Expected: PASS nos 3 testes.

- [ ] **Step 8: Rodar a suíte inteira + gate do repo**

Run: `gofmt -l . && go vet ./... && go build ./... && go test ./...`
Expected: `gofmt -l .` sem saída; tudo verde.

- [ ] **Step 9: Commit**

```bash
git add apps/api-go/portal_models.go apps/api-go/portal_chamada.go apps/api-go/handlers_portal_chamada.go apps/api-go/portal_routes.go apps/api-go/handlers_portal_test.go
git commit -m "feat(portal): GET /portal/me/sessions — histórico do próprio aluno"
```

---

### Task 5: Deploy e confirmação

- [ ] **Step 1: Push da branch e abertura de PR**

```bash
git push -u origin claude-henrique/aluno-particular-aulas-contratadas
gh pr create --title "feat(portal): aulas contratadas + histórico do aluno particular" --body "Back-end da spec dashboard/docs/superpowers/specs/2026-09-09-aluno-particular-aulas-contratadas-historico-design.md: campo enrollment.contracted_lessons, PATCH estendido, e GET /portal/me/sessions self-service."
```

- [ ] **Step 2: Confirmar o pipeline de deploy deste repo antes de mesclar**

Este plano assume que existe checagem de CI (`gofmt`/`vet`/`build`/`test`) rodando
no PR — confirme no `gh pr checks` antes de pedir revisão/mesclar. **Verificar
manualmente** (não documentado neste plano ainda) se `master` tem deploy automático
como o `dashboard` tem — se não tiver, o passo de deploy é manual e fica pendência
registrada no `PENDENCIAS.md` do `dashboard` até confirmado.

- [ ] **Step 3: Depois de mesclado e deployado, confirmar em produção**

```bash
curl -s -o /dev/null -w "%{http_code}\n" https://api.santos-tech.com/portal/me/sessions
```

Expected: `401` (rota existe e exige auth — confirma que o deploy pegou a rota
nova; um teste funcional completo exige sessão real, fora do escopo deste curl).

Só depois de confirmado aqui, o plano do front-end
(`dashboard/docs/superpowers/plans/2026-09-09-aluno-particular-aulas-contratadas-historico.md`)
pode consumir os campos/endpoint novos com segurança.
