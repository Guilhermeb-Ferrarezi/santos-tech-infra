# Checklist de confirmação por dono de plataforma — Backend (api-go) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** restringir o checklist de confirmação de publicação (feature de 13/08) pra que cada
checkbox só possa ser marcado/desmarcado por um "dono" configurado daquela plataforma — um
mapeamento global, admin-only, `platform → user`. Ver spec completa em
`dashboard/docs/superpowers/specs/2026-08-17-checklist-confirmacao-por-dono-design.md`
(vive no repo `dashboard`, cobre backend + frontend juntos — link relativo omitido de propósito:
a spec está numa branch de feature do `dashboard` ainda não mesclada em `main`, então um link
`../../../../dashboard/...` só resolve depois do merge).

**Architecture:** nova tabela `social_platform_owners` (chave = `platform`, sem `post_id` —
é global, não por post). Reaproveita 100% o padrão já existente de
`social_post_platform_confirmations` (mesmo arquivo `social.go`, mesmo estilo pgx cru, mesmo
`appErr`/`writeErr`). Os handlers de confirmar/desconfirmar (`handlers_social.go:317-376`)
ganham uma checagem extra de dono antes do upsert/delete — se houver dono configurado e o
usuário autenticado não for ele, `403`. Sem dono configurado, comportamento inalterado
(fail-open). 3 rotas novas: `GET` (qualquer um com `social:read`), `PUT`/`DELETE` (admin-only,
sem cargo novo).

**Tech Stack:** Go 1.25, `net/http` stdlib, `pgx/v5`, sem framework — mesmo padrão do resto de
`apps/api-go`.

**Pré-requisito:** rodar o E2E pendente da feature de 13/08 (`social-publish-confirmation.spec.ts`,
repo `dashboard`) antes de empilhar esta camada — ver Task 0.

---

### Task 0: Confirmar que a base de 13/08 funciona ponta-a-ponta

- [ ] **Step 1: Rodar o E2E pendente (repo `dashboard`)**

Run: `cd C:\Users\55169\Documents\GitHub\dashboard && cd web && bunx playwright install chromium && bunx playwright test social-publish-confirmation.spec.ts`
Expected: teste passa. Se falhar, **parar e corrigir a feature de 13/08 antes de continuar** —
não faz sentido construir a checagem de dono em cima de uma trava não verificada.

---

### Task 1: Migração — tabela `social_platform_owners`

**Files:**
- Modify: `apps/api-go/db.go:411-412` (logo depois do `CREATE INDEX` da tabela de confirmações,
  antes do fechamento da const string)

- [ ] **Step 1: Adicionar a tabela ao bloco de migração**

Inserir depois da linha 411 (`CREATE INDEX ... idx_social_post_platform_confirmations_post ...`):

```go
-- Dono por plataforma (global, não por post) — quem pode confirmar/desconfirmar aquele
-- canal no checklist de publicação. Sem linha = sem dono = qualquer um confirma (fail-open).
CREATE TABLE IF NOT EXISTS social_platform_owners (
  platform    TEXT PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  updated_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- [ ] **Step 2: Build**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro (é só uma const string Go, não roda migração de verdade aqui).

---

### Task 2: Camada de dados — `social.go`

**Files:**
- Modify: `apps/api-go/social.go` (struct perto de `SocialPostPublishConfirmation`, linha ~60;
  funções perto de `deleteSocialPostPublishConfirmation`, linha ~359)

- [ ] **Step 1: Struct `SocialPlatformOwner`**

Adicionar logo depois de `SocialPostPublishConfirmation` (após linha 60):

```go
type SocialPlatformOwner struct {
	Platform  string    `json:"platform"`
	UserID    int64     `json:"userId"`
	UserName  string    `json:"userName"`
	UpdatedAt time.Time `json:"updatedAt"`
}
```

- [ ] **Step 2: Funções de acesso — listar, buscar 1, definir, remover**

Adicionar depois de `deleteSocialPostPublishConfirmation` (após a linha 359 atual, antes de
`checkPublishConfirmationsComplete`):

```go
func (s *Server) listSocialPlatformOwners(ctx context.Context) ([]SocialPlatformOwner, error) {
	rows, err := s.db.Query(ctx, `
		SELECT o.platform, o.user_id, COALESCE(u.name,''), o.updated_at
		FROM social_platform_owners o
		JOIN users u ON u.id = o.user_id
		ORDER BY o.platform`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialPlatformOwner{}
	for rows.Next() {
		var o SocialPlatformOwner
		if err := rows.Scan(&o.Platform, &o.UserID, &o.UserName, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// getSocialPlatformOwner retorna nil (sem erro) se a plataforma não tiver dono configurado —
// esse é o caminho de fail-open, não uma condição de erro.
func (s *Server) getSocialPlatformOwner(ctx context.Context, platform string) (*SocialPlatformOwner, error) {
	var o SocialPlatformOwner
	err := s.db.QueryRow(ctx, `
		SELECT o.platform, o.user_id, COALESCE(u.name,''), o.updated_at
		FROM social_platform_owners o
		JOIN users u ON u.id = o.user_id
		WHERE o.platform = $1`, platform).
		Scan(&o.Platform, &o.UserID, &o.UserName, &o.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}

// setSocialPlatformOwner grava/atualiza o dono (upsert). updatedBy é sempre o usuário
// autenticado (chamador nunca aceita esse valor do cliente) — mesma convenção da confirmação.
func (s *Server) setSocialPlatformOwner(ctx context.Context, platform string, userID, updatedBy int64) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO social_platform_owners (platform, user_id, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (platform) DO UPDATE SET user_id = $2, updated_by = $3, updated_at = now()`,
		platform, userID, updatedBy)
	return err
}

func (s *Server) deleteSocialPlatformOwner(ctx context.Context, platform string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM social_platform_owners WHERE platform = $1`, platform)
	return err
}
```

Checar o topo de `social.go` — se `errors` e `pgx` (`github.com/jackc/pgx/v5`) já estiverem
importados (prováveis, dado o uso de `pgx.ErrNoRows` em outros arquivos do pacote), não precisa
adicionar import novo; senão, adicionar.

- [ ] **Step 2: Build**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro.

---

### Task 3: Handlers — checar dono ao confirmar/desconfirmar + CRUD de `platform-owners`

**Files:**
- Modify: `apps/api-go/handlers_social.go:317-376` (checagem de dono nos 2 handlers existentes)
- Modify: `apps/api-go/handlers_social.go` (3 handlers novos, adicionar no fim do arquivo)

- [ ] **Step 1: Checagem de dono em `handleConfirmSocialPostPlatform`**

Em `handlers_social.go:317-347`, inserir a checagem **depois** da validação de plataforma
(linha 324-327) e **antes** de `s.upsertSocialPostPublishConfirmation` (linha 337):

```go
	owner, err := s.getSocialPlatformOwner(r.Context(), platform)
	if err != nil {
		writeErr(w, err)
		return
	}
	if owner != nil && owner.UserID != userIDFrom(r) {
		writeErr(w, appErr(http.StatusForbidden, "NOT_PLATFORM_OWNER",
			fmt.Sprintf("Só %s pode confirmar esta plataforma.", owner.UserName)))
		return
	}
```

- [ ] **Step 2: Mesma checagem em `handleUnconfirmSocialPostPlatform`**

Em `handlers_social.go:350-376`, inserir a **mesma checagem** (validando também a plataforma
com `validSocialPlatforms[platform]`, que hoje falta nesse handler — bug pré-existente pequeno,
corrigir de passagem) logo após obter `platform` (linha 356) e antes de
`s.deleteSocialPostPublishConfirmation` (linha 366):

```go
	if !validSocialPlatforms[platform] {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Plataforma inválida"))
		return
	}
	owner, err := s.getSocialPlatformOwner(r.Context(), platform)
	if err != nil {
		writeErr(w, err)
		return
	}
	if owner != nil && owner.UserID != userIDFrom(r) {
		writeErr(w, appErr(http.StatusForbidden, "NOT_PLATFORM_OWNER",
			fmt.Sprintf("Só %s pode confirmar esta plataforma.", owner.UserName)))
		return
	}
```

- [ ] **Step 3: 3 handlers novos — listar, definir, remover dono**

Adicionar no fim de `handlers_social.go`:

```go
// GET /social/platform-owners
func (s *Server) handleListSocialPlatformOwners(w http.ResponseWriter, r *http.Request) {
	owners, err := s.listSocialPlatformOwners(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": owners})
}

// PUT /social/platform-owners/{platform}
func (s *Server) handleSetSocialPlatformOwner(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	if !validSocialPlatforms[platform] {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Plataforma inválida"))
		return
	}
	var in struct {
		UserID int64 `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.UserID <= 0 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	target, err := s.cachedUserByID(r.Context(), in.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if target == nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Usuário não encontrado"))
		return
	}
	if err := s.setSocialPlatformOwner(r.Context(), platform, in.UserID, userIDFrom(r)); err != nil {
		writeErr(w, err)
		return
	}
	owners, err := s.listSocialPlatformOwners(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": owners})
}

// DELETE /social/platform-owners/{platform}
func (s *Server) handleDeleteSocialPlatformOwner(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	if !validSocialPlatforms[platform] {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Plataforma inválida"))
		return
	}
	if err := s.deleteSocialPlatformOwner(r.Context(), platform); err != nil {
		writeErr(w, err)
		return
	}
	owners, err := s.listSocialPlatformOwners(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": owners})
}
```

Checar se `encoding/json` já está importado no arquivo (bem provável, dado `json.NewDecoder`
usado em outros handlers do mesmo arquivo, ex. linha ~299).

- [ ] **Step 4: Build**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro. Se `fmt` não estiver importado em `handlers_social.go`, adicionar.

---

### Task 4: Rotas — `routes.go`

**Files:**
- Modify: `apps/api-go/routes.go:209` (adicionar logo depois)

- [ ] **Step 1: Registrar as 3 rotas novas**

Depois da linha 209 (`DELETE /social/posts/{id}/publish-confirmations/{platform}`):

```go
	mux.HandleFunc("GET /social/platform-owners", s.permGuard("social", "read", false, s.handleListSocialPlatformOwners))
	mux.HandleFunc("PUT /social/platform-owners/{platform}", s.rateLimit(30, min, s.adminGuard(s.handleSetSocialPlatformOwner)))
	mux.HandleFunc("DELETE /social/platform-owners/{platform}", s.adminGuard(s.handleDeleteSocialPlatformOwner))
```

`GET` segue o mesmo padrão de leitura do resto do módulo (`social:read`, `allowTeacher=false`);
`PUT`/`DELETE` são **admin-only** (decisão 6 da spec — não cria cargo novo pra isso).

- [ ] **Step 2: Build**

Run: `cd apps/api-go && go build ./...`
Expected: sem erro.

---

### Task 5: Testes

**Files:**
- Modify: `apps/api-go/handlers_social_test.go`

- [ ] **Step 1: Testes de unidade**

Seguir o harness existente (`testServer(Config{})`, `reqAs(r, userID)`, `validUUID`,
`socialPlatformReq` — ver `handlers_social_test.go:12-65`). Como `testServer` roda com
`s.db == nil`, os testes que dependem de banco real (upsert/delete/list de owners) precisam do
mesmo padrão de guard usado em `TestCheckPublishConfirmationsCompleteNoTargets` (testar só o
caminho que não toca o banco) — ou, se o repo já tiver um harness de integração com Postgres
real pra outros testes de `social.go`, seguir esse padrão em vez de pular. Cobrir:

- `TestConfirmPlatformForbiddenWhenNotOwner` — dono configurado ≠ usuário da requisição → `403 NOT_PLATFORM_OWNER`.
- `TestConfirmPlatformAllowedWhenOwner` — usuário da requisição == dono → sucesso.
- `TestConfirmPlatformAllowedWhenNoOwnerConfigured` — sem linha em `social_platform_owners` → sucesso (fail-open).
- `TestUnconfirmPlatformSameOwnerRule` — mesma regra vale pro DELETE.
- `TestSetPlatformOwnerAdminOnly` — role não-admin (mesmo com `social:execute`) → `403` no `PUT`/`DELETE /social/platform-owners/{platform}`.
- `TestListPlatformOwnersAnyoneWithSocialRead` — `GET` funciona pra quem só tem `social:read`.

- [ ] **Step 2: Rodar os testes**

Run: `cd apps/api-go && go test ./... -run Social`
Expected: todos passam.

---

### Task 6: Documentação — `llms.txt`

**Files:**
- Modify: `apps/api-go/llms.txt:334-338` (logo depois da linha da trava de publicação)

- [ ] **Step 1: Adicionar a documentação das 3 rotas novas**

Depois da linha 338 (fim do bullet "Trava de publicação"), antes do parágrafo `**SocialPost**`
(linha 340), adicionar:

```
- `GET /social/platform-owners` — mapeamento dono→plataforma. → `{owners:[SocialPlatformOwner]}`
- `PUT /social/platform-owners/{platform} {userId}` — define o dono (upsert). **Admin-only**
  (não é cargo `social`). → `{owners:[...]}` · plataforma/userId inválido → 400
- `DELETE /social/platform-owners/{platform}` — remove o dono (volta pro modelo de confiança,
  qualquer um confirma). **Admin-only**. → `{owners:[...]}`
- **Dono da plataforma**: se configurado, só ele confirma/desconfirma aquele item do checklist
  de publicação (`POST`/`DELETE .../publish-confirmations/{platform}` → 403 `NOT_PLATFORM_OWNER`
  pra qualquer outro usuário). Sem dono configurado, comportamento de 13/08 (qualquer um com
  `social:execute` confirma). Feature de 17/08/2026.
```

E adicionar a linha do schema `SocialPlatformOwner` perto do parágrafo `**SocialPost**`:

```
**SocialPlatformOwner**: `{platform, userId, userName, updatedAt}` — mapeamento global
(não por post) de quem pode confirmar aquele canal.
```

---

### Task 7: Verificação final e commit

- [ ] **Step 1: gofmt + vet + build + test**

Run:
```bash
cd apps/api-go
gofmt -l .
go vet ./...
go build ./...
go test ./...
```
Expected: `gofmt -l .` sem saída; os demais sem erro.

- [ ] **Step 2: Commit**

```bash
cd apps/api-go
git add db.go social.go handlers_social.go handlers_social_test.go routes.go llms.txt
git commit -m "feat(social): dono por plataforma no checklist de confirmacao"
```

- [ ] **Step 3: Deploy**

Push conforme o fluxo padrão do repo. Após o deploy, confirmar em produção com uma chamada
real: `curl` autenticado em `GET https://api.santos-tech.com/social/platform-owners` deve
retornar `{"owners":[]}` (mapeamento vazio, ninguém configurado ainda — próximo passo é o
frontend, que dá a tela pra atribuir os primeiros donos).
