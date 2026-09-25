# Acesso remoto da frota — Plano 1 (backend, watchdog, MCP) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Permissões individuais por usuário, rotas de dispositivos por permissão, fila de comandos auditada com long-poll, watchdog que entrega comando em ~1s, e tools de dispositivos no MCP — ao final, o Gabriel (user 55) opera a frota pelo MCP sem ser admin.

**Architecture:** `users.permissions` (JSONB) somado ao cargo numa regra única (`effectivePerms`/`hasPerm`) usada por `permGuard`. Uma tabela `hour_lab_device_commands` vira fila + trilha de auditoria; heartbeat e o novo `POST /public/lab-devices/wait-command` (long-poll de até 50s) entregam o comando pendente mais antigo, uma única vez (at-most-once — comando roda como SYSTEM, reexecutar é pior que perder). O watchdog PowerShell troca o `Start-Sleep 60` pelo long-poll. O mcp-go ganha `tools_devices.go` repassando o token do usuário.

**Tech Stack:** Go 1.25 (`net/http`, pgx/v5, sqlc, go-redis, miniredis nos testes), PowerShell 5.1 (watchdog), MCP go-sdk (mcp-go), Postgres 16, Cloudflare R2.

**Spec:** `docs/superpowers/specs/2026-09-25-acesso-remoto-frota-design.md`

**Fora deste plano (Plano 2):** SSH por usuário (spec §3), front do `dashboard` e o `dashboard/api` passando a usar `permissions` pra qualquer role (spec §1 "Consumidores" e §6), bug do `CloseRead` no shell (spec §7, 1º item).

**Desvio consciente da spec:** a tool `device_screenshot` (spec §5) fica fora — print de tela hoje só é tirado pelo hour-timer-app, que não está em todos os PCs; o watchdog não tira print. Entra quando o watchdog ganhar captura.

## Global Constraints

- Go: `go` e `sqlc` estão em `~/.local/bin` (use `PATH=$PATH:$HOME/.local/bin`). Gate antes de TODO commit: `gofmt -l .` (vazio), `go vet ./...`, `go build ./...`, `go test ./...` no serviço tocado.
- Todo SQL novo em `apps/api-go/db/query/*.sql` + `sqlc generate` (padrão 6 do `CLAUDE.md`). Exceção consciente: nenhuma — o SQL legado inline de `hour_lab_devices.go` fica como está.
- Rotas novas/alteradas → `docs/openapi.yaml` e `apps/api-go/llms.txt` (Task 8).
- Commits: mensagem no imperativo com escopo (`feat(api-go): ...`), **sem** linha `Co-Authored-By` (preferência do Guilherme).
- Segredos: nunca colar token/senha em arquivo do repo. Token do R2 = memória `reference_cloudflare_r2_santos_tech`; banco = memória `reference_frota_comando_remoto_via_db`.
- Nomes de permissão: recurso `dispositivos`, ações `ver`, `controlar`, `executar`, `ssh`, `gerenciar`. Chaves/ações validam `^[a-z_]{1,40}$`, máx. 60 recursos × 10 ações.
- `appVersion` do watchdog: `"wnsh-watchdog"` (literal usado pelo `wnsh-loop.ps1`).
- Long-poll: máx. **50s** (o `WriteTimeout` da api-go é 60s — `main.go:190`), checagem a cada **1s**, rate limit 120/min.
- Valores de `source` na auditoria: `painel` (cookie), `mcp_oauth` (JWT com `aud`), `pat` (`st_...`), `api` (Bearer JWT sem `aud`).

## Review Focus

1. **Dois admins mandam comando pro mesmo PC em sequência** → os dois rodam, na ordem, cada um com seu resultado (hoje o segundo apaga o primeiro). Teste: Task 5, `TestWaitForLabCommandEntregaEmOrdem` + fake com 2 itens.
2. **Resposta do heartbeat se perde / watchdog reinicia no meio** → o comando NÃO roda duas vezes (at-most-once). Teste: Task 4, `TestFakeClaimNextEntregaUmaVezSo` documenta o contrato; SQL usa `delivered_at IS NULL` + `SKIP LOCKED`.
3. **PC com hour-timer-app E watchdog** → só o watchdog recebe comando. Teste: Task 5, `TestShouldDeliverCommand`.
4. **Usuário sem permissão (ou só `ver`) chama `/command` pelo MCP** → 403, nada é enfileirado. Teste: Task 7, `TestDispositivosExecutarNegaSemPermissao`.
5. **Admin edita permissões sem sudo recente, ou manda JSON de permissões lixo** → 403 `SUDO_REQUIRED` / 400, nada gravado. Teste: Task 3.

---

### Task 1: Versionar os scripts da frota em `infra/fleet/`

**Files:**
- Create: `infra/fleet/wnsh-loop.ps1` (cópia byte a byte do que está no CDN)
- Create: `infra/fleet/collect-apps.ps1`
- Create: `infra/fleet/fix-ssh-santos-fleet.ps1`
- Create: `infra/fleet/check-ps1.sh`
- Create: `infra/fleet/README.md`

**Interfaces:**
- Produces: `infra/fleet/check-ps1.sh <arquivo.ps1>...` → imprime `OK <arquivo>` ou os erros de parse e sai ≠ 0. Usado nas Tasks 9.

- [ ] **Step 1: Copiar os arquivos**

```bash
cd ~/Projetos/santos-tech-infra && mkdir -p infra/fleet
curl -fsS https://cdn.santos-tech.com/downloads/wnsh-loop.ps1 -o infra/fleet/wnsh-loop.ps1
SP=/tmp/claude-1000/-home-guilherme-Projetos/ff6c0f54-cc77-45e8-aa5f-3bad3d41203e/scratchpad
cp $SP/frota/collect-apps.ps1 infra/fleet/collect-apps.ps1
cp $SP/fix-ssh.ps1 infra/fleet/fix-ssh-santos-fleet.ps1
sha256sum infra/fleet/wnsh-loop.ps1   # esperado: c18d8c63bdee61bd314690b63365782c600cb7e541c9b08a1558c36545c5b99f
```
Se o scratchpad não existir mais: `scp -i ~/.ssh/id_ed25519_fleet_admin santos-fleet@100.118.245.16:C:/ProgramData/SantosTech/collect-apps.ps1 infra/fleet/`; o `fix-ssh-santos-fleet.ps1` é o da memória `reference_fleet_admin_ssh_key` (versão com `(?m)^Match\s`, sem `/T`, com `/setowner`).

- [ ] **Step 2: Escrever o verificador de sintaxe**

`infra/fleet/check-ps1.sh`:
```bash
#!/usr/bin/env bash
# Valida sintaxe de .ps1 com o parser real do PowerShell (container oficial).
# Um erro de sintaxe no wnsh-loop.ps1 derruba o heartbeat da frota INTEIRA
# (ele se auto-atualiza pelo CDN) — rodar SEMPRE antes de publicar.
set -euo pipefail
[ $# -ge 1 ] || { echo "uso: $0 arquivo.ps1..." >&2; exit 2; }
dir=$(cd "$(dirname "$1")" && pwd)
names=(); for f in "$@"; do names+=("$(basename "$f")"); done
docker run --rm -v "$dir":/w:ro mcr.microsoft.com/powershell:latest pwsh -NoProfile -c '
  $bad=0
  foreach ($f in $args) {
    $e=$null; $t=$null
    [void][System.Management.Automation.Language.Parser]::ParseFile("/w/$f",[ref]$t,[ref]$e)
    if ($e) { $bad=1; $e | ForEach-Object { "ERRO {0}:{1}: {2}" -f $f,$_.Extent.StartLineNumber,$_.Message } } else { "OK $f" }
  }
  exit $bad' "${names[@]}"
```

- [ ] **Step 3: Rodar o verificador**

Run: `chmod +x infra/fleet/check-ps1.sh && infra/fleet/check-ps1.sh infra/fleet/wnsh-loop.ps1 infra/fleet/collect-apps.ps1 infra/fleet/fix-ssh-santos-fleet.ps1`
Expected: três linhas `OK ...`, exit 0.

- [ ] **Step 4: README**

`infra/fleet/README.md`:
```markdown
# Scripts da frota (PCs do laboratório)

Fonte da verdade dos scripts que rodam como SYSTEM nos PCs Windows.

| Arquivo | Onde roda | Publicação |
|---|---|---|
| `wnsh-loop.ps1` | serviço `WinNetSvcHost` (NSSM → `svchelper.exe`), `C:\ProgramData\SantosTech\` | R2 `downloads/wnsh-loop.ps1` — os PCs se auto-atualizam a cada volta (~60s) comparando sha256 |
| `collect-apps.ps1` | tarefa `/it` temporária disparada pelo loop | copiado pelo instalador |
| `fix-ssh-santos-fleet.ps1` | uma vez por PC, via canal de comando (SYSTEM) | manual |

## Publicar o `wnsh-loop.ps1`

1. `infra/fleet/check-ps1.sh infra/fleet/wnsh-loop.ps1` — tem que dar `OK`.
2. **Canário primeiro** (gazake-1, que tem SSH de fallback): gere a variante com
   `$updateUrl` apontando pra `downloads/wnsh-loop-canary.ps1`, suba nessa chave e
   copie pro PC (`scp` como `santos-fleet`). Confirme heartbeat + um comando.
3. Suba o arquivo final em `downloads/wnsh-loop.ps1` e o MESMO conteúdo em
   `downloads/wnsh-loop-canary.ps1` (o canário volta pro canal normal sozinho).

Upload (token na memória `reference_cloudflare_r2_santos_tech`, nunca no repo):
    curl -X PUT -H "Authorization: Bearer $CF_R2_TOKEN" -H "Content-Type: text/plain" \
      --data-binary @infra/fleet/wnsh-loop.ps1 \
      "https://api.cloudflare.com/client/v4/accounts/ac17de2ec0cc4e48dc21ddf2eeb5a879/r2/buckets/santos-tech/objects/downloads/wnsh-loop.ps1"

## Risco conhecido

Quem escreve em `downloads/wnsh-loop.ps1` no R2 executa como SYSTEM em toda a frota.
```

- [ ] **Step 5: Commit**

```bash
git add infra/fleet && git commit -m "chore(fleet): versiona os scripts do watchdog e do SSH da frota"
```

---

### Task 2: Permissões individuais — modelo e regra única

**Files:**
- Create: `apps/api-go/permissions.go`
- Create: `apps/api-go/permissions_test.go`
- Modify: `apps/api-go/models.go` (struct `User`, `UserProfile`)
- Modify: `apps/api-go/db.go` (constante `migration` ~linha 56; `userCols` linha 1155; `scanUser` linha 1160; `buildProfile` linha 1458)
- Modify: `apps/api-go/db/schema.sql` (tabela `users`)
- Modify: `apps/api-go/portal_guards.go` (`permGuard` linha 64, `portalAnyRead` linha 132)
- Modify: `apps/api-go/handlers_oauth_provider.go:355`

**Interfaces:**
- Produces:
  - `func validatePermissions(p map[string][]string) error`
  - `func mergePerms(a, b map[string][]string) map[string][]string`
  - `func hasPerm(u *User, rolePerms map[string][]string, resource, action string, allowTeacher bool) bool`
  - `func (s *Server) rolePermsOf(ctx context.Context, u *User) map[string][]string`
  - `func (s *Server) effectivePerms(ctx context.Context, u *User) map[string][]string` (nil para admin)
  - Campo `User.Permissions map[string][]string` e `UserProfile.UserPermissions map[string][]string` (`json:"userPermissions"`).

- [ ] **Step 1: Testes que falham**

`apps/api-go/permissions_test.go`:
```go
package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidatePermissions(t *testing.T) {
	ok := map[string][]string{"dispositivos": {"ver", "executar"}, "portal_cursos": {"read"}}
	if err := validatePermissions(ok); err != nil {
		t.Fatalf("válido recusado: %v", err)
	}
	ruins := []map[string][]string{
		{"Dispositivos": {"ver"}},                     // maiúscula
		{"dispositivos": {"ver;drop"}},                // caractere inválido
		{"": {"ver"}},                                 // vazio
		{"dispositivos": {strings.Repeat("a", 41)}},   // longo demais
		{"x": {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}}, // 11 ações
	}
	for i, p := range ruins {
		if err := validatePermissions(p); err == nil {
			t.Fatalf("caso %d deveria falhar: %v", i, p)
		}
	}
	muitos := map[string][]string{}
	for i := 0; i < 61; i++ {
		muitos[strings.Repeat("r", 1)+string(rune('a'+i%26))+strings.Repeat("x", i/26)] = []string{"ver"}
	}
	if err := validatePermissions(muitos); err == nil {
		t.Fatal("61 recursos deveria falhar")
	}
}

func TestMergePermsUneSemDuplicar(t *testing.T) {
	a := map[string][]string{"dispositivos": {"ver"}, "agenda": {"read"}}
	b := map[string][]string{"dispositivos": {"ver", "executar"}}
	got := mergePerms(a, b)
	want := map[string][]string{"dispositivos": {"ver", "executar"}, "agenda": {"read"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if mergePerms(nil, nil) == nil {
		t.Fatal("merge de nils deve devolver mapa vazio, não nil")
	}
}

func TestHasPerm(t *testing.T) {
	cargo := map[string][]string{"agenda": {"read"}}
	cases := []struct {
		nome         string
		u            *User
		rolePerms    map[string][]string
		res, act     string
		allowTeacher bool
		want         bool
	}{
		{"nil nega", nil, nil, "dispositivos", "ver", false, false},
		{"admin passa sempre", &User{Role: RoleAdmin}, nil, "dispositivos", "executar", false, true},
		{"professor com allowTeacher", &User{Role: RoleTeacher}, nil, "agenda", "read", true, true},
		{"professor sem allowTeacher e sem perm", &User{Role: RoleTeacher}, nil, "agenda", "read", false, false},
		{"individual concede a aluno", &User{Role: RoleStudent, Permissions: map[string][]string{"dispositivos": {"ver"}}}, nil, "dispositivos", "ver", false, true},
		{"individual não vaza pra outra ação", &User{Role: RoleCustom, Permissions: map[string][]string{"dispositivos": {"ver"}}}, nil, "dispositivos", "executar", false, false},
		{"cargo concede", &User{Role: RoleCustom}, cargo, "agenda", "read", false, true},
		{"soma cargo + individual", &User{Role: RoleCustom, Permissions: map[string][]string{"dispositivos": {"executar"}}}, cargo, "dispositivos", "executar", false, true},
	}
	for _, c := range cases {
		if got := hasPerm(c.u, c.rolePerms, c.res, c.act, c.allowTeacher); got != c.want {
			t.Errorf("%s: got %v want %v", c.nome, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin go test -run 'TestValidatePermissions|TestMergePerms|TestHasPerm' ./...`
Expected: FAIL — `undefined: validatePermissions` (e `User.Permissions`).

- [ ] **Step 3: Implementar `permissions.go`**

```go
package main

import (
	"context"
	"fmt"
	"regexp"
	"slices"
)

// Permissões por recurso/ação no formato de custom_roles.permissions:
// {"dispositivos": ["ver", "executar"]}. A permissão EFETIVA de um usuário é a
// união das individuais (users.permissions) com as do cargo (só role 4 com
// custom_role_id). Admin passa sempre, sem consultar nada.

var permNameRe = regexp.MustCompile(`^[a-z_]{1,40}$`)

const (
	maxPermResources = 60
	maxPermActions   = 10
)

func validatePermissions(p map[string][]string) error {
	if len(p) > maxPermResources {
		return fmt.Errorf("no máximo %d recursos", maxPermResources)
	}
	for res, acts := range p {
		if !permNameRe.MatchString(res) {
			return fmt.Errorf("recurso inválido: %q", res)
		}
		if len(acts) > maxPermActions {
			return fmt.Errorf("recurso %q: no máximo %d ações", res, maxPermActions)
		}
		for _, a := range acts {
			if !permNameRe.MatchString(a) {
				return fmt.Errorf("ação inválida em %q: %q", res, a)
			}
		}
	}
	return nil
}

func mergePerms(a, b map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, m := range []map[string][]string{a, b} {
		for res, acts := range m {
			for _, act := range acts {
				if !slices.Contains(out[res], act) {
					out[res] = append(out[res], act)
				}
			}
		}
	}
	return out
}

// hasPerm é a regra única de autorização por recurso/ação.
func hasPerm(u *User, rolePerms map[string][]string, resource, action string, allowTeacher bool) bool {
	if u == nil {
		return false
	}
	if u.Role == RoleAdmin {
		return true
	}
	if allowTeacher && u.Role == RoleTeacher {
		return true
	}
	return slices.Contains(u.Permissions[resource], action) || slices.Contains(rolePerms[resource], action)
}

// rolePermsOf carrega as permissões do cargo (cache Redis de 2min). Falha de
// leitura vira "sem permissões de cargo" — nega, nunca libera.
func (s *Server) rolePermsOf(ctx context.Context, u *User) map[string][]string {
	if u == nil || u.Role != RoleCustom || u.CustomRoleID == nil {
		return nil
	}
	cr, err := s.cachedCustomRole(ctx, *u.CustomRoleID)
	if err != nil || cr == nil {
		return nil
	}
	return cr.Permissions
}

// effectivePerms: o que o usuário pode, pra exibir (/auth/me) e checar em
// massa. nil para admin (convenção antiga: admin = tudo).
func (s *Server) effectivePerms(ctx context.Context, u *User) map[string][]string {
	if u == nil || u.Role == RoleAdmin {
		return nil
	}
	return mergePerms(u.Permissions, s.rolePermsOf(ctx, u))
}
```

- [ ] **Step 4: Modelo, migração e scan**

`models.go`, no struct `User` (depois de `LoginDisabled`):
```go
	Permissions     map[string][]string // permissões individuais (users.permissions); somam com as do cargo
```
No struct `UserProfile` (depois de `Permissions`):
```go
	UserPermissions map[string][]string `json:"userPermissions"` // só as individuais (tela de edição)
```
`db.go`, na constante `migration`, logo após a linha 56 (`ALTER TABLE users ADD COLUMN IF NOT EXISTS login_disabled ...`):
```sql
-- Permissões individuais (somam com as do cargo). Mesmo formato de custom_roles.permissions.
ALTER TABLE users ADD COLUMN IF NOT EXISTS permissions JSONB NOT NULL DEFAULT '{}';
```
`db.go:1155` — acrescentar `permissions` ao FIM de `userCols`:
```go
const userCols = `id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled, permissions`
```
`scanUser` (`db.go:1160`) — acrescentar `&u.Permissions` ao fim do `Scan` (pgx decodifica jsonb direto no map):
```go
	err := row.Scan(&u.ID, &u.Email, &u.Username, &u.Name, &u.PasswordHash, &u.AvatarURL,
		&u.Role, &u.CustomRoleID, &u.MFAEnabled, &u.TOTPSecret, &u.SuspendedAt, &u.CreatedAt, &u.Preferences, &u.QuotaBytes, &u.EmailVerifiedAt, &u.MFAMethod, &u.LoginDisabled, &u.Permissions)
```
Conferir que TODOS os usos de `userCols`/`userCols2` passam por `scanUser` (são 12: `grep -n "userCols" apps/api-go/*.go`) — nenhum faz Scan manual.
`db/schema.sql`, na tabela `users`, acrescentar a coluna: `permissions JSONB NOT NULL DEFAULT '{}',`.

`buildProfile` (`db.go:1458`) — trocar o bloco `if u.Role == RoleCustom && u.CustomRoleID != nil { ... }` por:
```go
	if u.Role != RoleAdmin {
		p.Permissions = s.effectivePerms(ctx, u)
		p.UserPermissions = u.Permissions
	}
```

- [ ] **Step 5: Guards usam a regra única**

`portal_guards.go`, corpo do `permGuard` — trocar os três `if` internos por:
```go
		if hasPerm(u, s.rolePermsOf(r.Context(), u), resource, action, allowTeacher) {
			next(w, r)
			return
		}
```
(atualizar o comentário do `permGuard`: "admin (sempre), professor (se allowTeacher), ou quem tiver resource:action nas permissões individuais ou do cargo").
`portalAnyRead` — trocar o bloco `if u.Role == RoleCustom && u.CustomRoleID != nil { ... }` por:
```go
			for res, acts := range s.effectivePerms(r.Context(), u) {
				if strings.HasPrefix(res, "portal_") && slices.Contains(acts, "read") {
					next(w, r)
					return
				}
			}
```
(adicionar `"slices"` aos imports). Em `handlers_oauth_provider.go:344-365` (`canAuthorizeOAuthClient`), trocar a leitura `s.cachedCustomRole(...)` + `cr.Permissions["oauth_clients"]` por `perms := s.effectivePerms(ctx, u)` e `perms["oauth_clients"]`, mantendo o resto da lógica (lista de client IDs ou `"*"`). `customRoleHasPerm` fica sem uso → apagar.

- [ ] **Step 6: Rodar testes e gate**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin bash -c 'gofmt -l . ; go vet ./... && go build ./... && go test ./...'`
Expected: `gofmt` sem saída, todos os testes PASS (inclusive os `TestPermGuard*NoToken` existentes).

- [ ] **Step 7: Commit**

```bash
git add apps/api-go && git commit -m "feat(api-go): permissões individuais por usuário somadas ao cargo"
```

---

### Task 3: Editar permissões do usuário (com sudo) e cargo opcional

**Files:**
- Modify: `apps/api-go/handlers_admin_users.go` (`handleUpdateAdminUser`, linha 180; `adminUserJSON`)
- Modify: `apps/api-go/db.go` (`updateAdminUserFull`, linha 1374)
- Modify: `apps/api-go/handlers_sudo.go` (novo helper)
- Test: `apps/api-go/handlers_admin_users_perm_test.go`

**Interfaces:**
- Consumes: `validatePermissions` (Task 2).
- Produces: `func (s *Server) requestHasSudo(r *http.Request) bool`; `PATCH /auth/admin/users/{id}` aceita `"permissions": {...}`; resposta `user.permissions`.

- [ ] **Step 1: Testes que falham**

`apps/api-go/handlers_admin_users_perm_test.go`:
```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func patchUserReq(body string) *http.Request {
	r := httptest.NewRequest("PATCH", "/auth/admin/users/55", strings.NewReader(body))
	r.SetPathValue("id", "55")
	return reqAs(r, 1)
}

func TestPatchUserPermissoesInvalidas400(t *testing.T) {
	s := testServer(Config{JWTSecret: "segredo-teste"})
	w := httptest.NewRecorder()
	s.handleUpdateAdminUser(w, patchUserReq(`{"permissions":{"Dispositivos":["ver"]}}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
}

func TestPatchUserPermissoesSemSudo403(t *testing.T) {
	s := testServer(Config{JWTSecret: "segredo-teste"})
	w := httptest.NewRecorder()
	s.handleUpdateAdminUser(w, patchUserReq(`{"permissions":{"dispositivos":["ver"]}}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
	var e struct{ Code string }
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	if e.Code != "SUDO_REQUIRED" {
		t.Fatalf("code de erro=%q", e.Code)
	}
}

func TestPatchUserRole4SemCargoNaoE400(t *testing.T) {
	// Sem banco (s.db nil) a atualização em si falha depois — o que importa é
	// NÃO barrar na validação ("customRoleId obrigatório" deixou de existir).
	s := testServer(Config{JWTSecret: "segredo-teste"})
	w := httptest.NewRecorder()
	func() {
		defer func() { _ = recover() }() // s.db nil pode entrar em pânico ao chegar no banco
		s.handleUpdateAdminUser(w, patchUserReq(`{"role":4}`))
	}()
	if w.Code == http.StatusBadRequest {
		t.Fatalf("role=4 sem customRoleId não pode mais ser 400: %s", w.Body)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin go test -run 'TestPatchUser' ./...`
Expected: FAIL — o JSON com `permissions` é ignorado hoje (sem 400/403) e `role:4` sem cargo dá 400.

- [ ] **Step 3: Helper de sudo**

`handlers_sudo.go`, logo abaixo de `sudoGuard`:
```go
// requestHasSudo diz se o token do request está elevado (sudo recente). Pra
// rotas em que só UMA parte do corpo exige sudo (ex.: PATCH de usuário muda
// nome sem sudo, mas permissões só com).
func (s *Server) requestHasSudo(r *http.Request) bool {
	token := ""
	if c, err := r.Cookie("access_token"); err == nil {
		token = c.Value
	}
	if token == "" {
		if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			token = after
		}
	}
	return !tokenSudoUntil(token, s.cfg.JWTSecret).Before(time.Now())
}
```
E refatorar `sudoGuard` pra usar: `if !s.requestHasSudo(r) { writeErr(...SUDO_REQUIRED...); return }`.

- [ ] **Step 4: Handler**

Em `handleUpdateAdminUser`, no `body` acrescentar:
```go
		Permissions  *map[string][]string `json:"permissions"`
```
Trocar a validação de `customRoleId` (bloco `if body.Role != nil && *body.Role == RoleCustom && (...)`) por:
```go
	// Cargo é opcional em role=4 (só as permissões individuais valem). Se vier, tem que ser UUID.
	if body.CustomRoleID != nil && *body.CustomRoleID != "" && !isValidUUID(*body.CustomRoleID) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "customRoleId deve ser UUID válido"))
		return
	}
	if body.CustomRoleID != nil && *body.CustomRoleID == "" {
		body.CustomRoleID = nil
	}
	var permsJSON []byte
	if body.Permissions != nil {
		if err := validatePermissions(*body.Permissions); err != nil {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "permissions: "+err.Error()))
			return
		}
		// Conceder dispositivos:executar = SYSTEM na frota inteira. Exige
		// confirmação recente de identidade, igual às ações destrutivas.
		if !s.requestHasSudo(r) {
			writeErr(w, appErr(http.StatusForbidden, "SUDO_REQUIRED", "Confirme sua identidade para alterar permissões"))
			return
		}
		permsJSON, _ = json.Marshal(*body.Permissions)
	}
```
(essas checagens ficam ANTES do `setUserSuspended`, para nada ser gravado se falharem). Passar `permsJSON` pra `updateAdminUserFull(..., body.CustomRoleID, permsJSON)`.

- [ ] **Step 5: SQL**

`updateAdminUserFull` ganha o parâmetro `permissions []byte` (nil = não mexe):
```go
func (s *Server) updateAdminUserFull(ctx context.Context, id int64, pwdHash string, name *string, role *int16, quotaBytes *int64, customRoleID *string, permissions []byte) (*User, error) {
```
e no UPDATE acrescentar a linha `permissions = COALESCE($6::jsonb, permissions),` com `permissions` como 6º argumento (`var permArg any; if permissions != nil { permArg = string(permissions) }` e passar `permArg`). `custom_role_id = CASE WHEN $3 = 4 THEN $5::uuid ...` continua — com `$5` NULL ele grava "sem cargo", que agora é válido.

`adminUserJSON(u)` ganha `"permissions": u.Permissions` (conferir o nome do map/struct que ele monta e acrescentar a chave).

- [ ] **Step 6: Testes + gate**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin bash -c 'gofmt -l . ; go vet ./... && go build ./... && go test ./...'`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add apps/api-go && git commit -m "feat(api-go): admin edita permissões individuais (com sudo) e cargo vira opcional"
```

---

### Task 4: Fila `hour_lab_device_commands` — schema, sqlc e store

**Files:**
- Modify: `apps/api-go/db.go` (constante `migration`, após o bloco de `hour_lab_device_screenshots` ~linha 772)
- Modify: `apps/api-go/db/schema.sql`
- Create: `apps/api-go/db/query/lab_commands.sql`
- Create (gerado): `apps/api-go/db/lab_commands.sql.go`
- Create: `apps/api-go/hour_lab_commands.go`
- Create: `apps/api-go/hour_lab_commands_test.go`
- Modify: `apps/api-go/server.go` (struct `Server` + `NewServer`, linha 56)

**Interfaces:**
- Produces:
```go
type LabCommand struct{ ID, Text string }
type LabCommandEntry struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Source      string     `json:"source"`
	Text        string     `json:"text"`
	Result      *string    `json:"result"`
	UserID      *int64     `json:"userId"`
	UserName    string     `json:"userName"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeliveredAt *time.Time `json:"deliveredAt"`
	ResultAt    *time.Time `json:"resultAt"`
}
type labCommandStore interface {
	Enqueue(ctx context.Context, deviceID string, userID int64, source, text string) (string, error)
	Audit(ctx context.Context, deviceID string, userID int64, source, kind, text string) error
	ClaimNext(ctx context.Context, deviceUUID string) (*LabCommand, error) // nil,nil = fila vazia
	StoreResult(ctx context.Context, deviceUUID, commandID, result string) (bool, error)
	List(ctx context.Context, deviceID string, limit int32) ([]LabCommandEntry, error)
	Get(ctx context.Context, deviceID, commandID string) (*LabCommandEntry, error) // nil,nil = não achou
}
```
  - Campo `Server.labCmds labCommandStore`; `fakeLabCmds` (em `hour_lab_commands_test.go`) para as Tasks 5–7.
  - `deviceID` = PK `hour_lab_devices.id` (uuid da URL do admin); `deviceUUID` = `device_uuid` (id que o PC conhece).

- [ ] **Step 1: Migração + schema**

Na constante `migration` (`db.go`), após `CREATE INDEX IF NOT EXISTS idx_hour_lab_screenshots_device ...`:
```sql
-- Fila + auditoria de ações remotas nos PCs (25/09/2026). kind='command' é
-- fila: entregue UMA vez (delivered_at) — roda como SYSTEM, reexecutar é pior
-- que perder. Os demais kinds são só trilha (quem travou/reiniciou/abriu shell).
CREATE TABLE IF NOT EXISTS hour_lab_device_commands (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  device_id    UUID NOT NULL REFERENCES hour_lab_devices(id) ON DELETE CASCADE,
  user_id      INTEGER REFERENCES users(id) ON DELETE SET NULL,
  source       TEXT NOT NULL,
  kind         TEXT NOT NULL,
  text         TEXT NOT NULL DEFAULT '',
  result       TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  delivered_at TIMESTAMPTZ,
  result_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_lab_cmds_pending
  ON hour_lab_device_commands(device_id, created_at) WHERE kind = 'command' AND delivered_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_lab_cmds_device ON hour_lab_device_commands(device_id, created_at DESC);
```
Mesmo `CREATE TABLE` (sem os comentários) em `db/schema.sql`, logo depois de `hour_lab_devices`.

- [ ] **Step 2: Queries**

`apps/api-go/db/query/lab_commands.sql`:
```sql
-- Fila/auditoria de ações remotas nos PCs do laboratório.

-- name: EnqueueLabDeviceCommand :one
INSERT INTO hour_lab_device_commands (device_id, user_id, source, kind, text)
VALUES (sqlc.arg(device_id)::uuid, sqlc.arg(user_id), sqlc.arg(source), 'command', sqlc.arg(text))
RETURNING id::text;

-- name: InsertLabDeviceAudit :exec
INSERT INTO hour_lab_device_commands (device_id, user_id, source, kind, text, delivered_at)
VALUES (sqlc.arg(device_id)::uuid, sqlc.arg(user_id), sqlc.arg(source), sqlc.arg(kind), sqlc.arg(text), now());

-- name: ClaimNextLabDeviceCommand :one
UPDATE hour_lab_device_commands c SET delivered_at = now()
WHERE c.id = (
  SELECT q.id FROM hour_lab_device_commands q
  JOIN hour_lab_devices d ON d.id = q.device_id
  WHERE d.device_uuid = sqlc.arg(device_uuid) AND q.kind = 'command' AND q.delivered_at IS NULL
  ORDER BY q.created_at
  LIMIT 1
  FOR UPDATE OF q SKIP LOCKED
)
RETURNING c.id::text, c.text;

-- name: StoreLabDeviceCommandQueueResult :execrows
UPDATE hour_lab_device_commands c SET result = sqlc.arg(result), result_at = now()
FROM hour_lab_devices d
WHERE d.id = c.device_id AND d.device_uuid = sqlc.arg(device_uuid)
  AND c.id = sqlc.arg(command_id)::uuid AND c.result_at IS NULL;

-- name: MirrorLabDeviceLegacyCommand :exec
-- Espelho nas colunas antigas até o front migrar pro /commands (Plano 2).
UPDATE hour_lab_devices
SET command_id = sqlc.arg(command_id)::uuid, command_text = sqlc.arg(text), command_sent_at = now(),
    command_result = NULL, command_result_at = NULL
WHERE id = sqlc.arg(device_id)::uuid;

-- name: ListLabDeviceCommands :many
SELECT c.id::text, c.kind, c.source, c.text, c.result, c.user_id,
       COALESCE(u.name, '')::text AS user_name, c.created_at, c.delivered_at, c.result_at
FROM hour_lab_device_commands c LEFT JOIN users u ON u.id = c.user_id
WHERE c.device_id = sqlc.arg(device_id)::uuid
ORDER BY c.created_at DESC
LIMIT sqlc.arg(lim);

-- name: GetLabDeviceCommand :one
SELECT c.id::text, c.kind, c.source, c.text, c.result, c.user_id,
       COALESCE(u.name, '')::text AS user_name, c.created_at, c.delivered_at, c.result_at
FROM hour_lab_device_commands c LEFT JOIN users u ON u.id = c.user_id
WHERE c.device_id = sqlc.arg(device_id)::uuid AND c.id = sqlc.arg(id)::uuid;
```

- [ ] **Step 3: Gerar**

Run: `cd apps/api-go && ~/.local/bin/sqlc generate && ls db/lab_commands.sql.go && grep -n "^func (q \*Queries)\|^type .*Params\|^type .*Row" db/lab_commands.sql.go`
Expected: métodos `EnqueueLabDeviceCommand`, `InsertLabDeviceAudit`, `ClaimNextLabDeviceCommand`, `StoreLabDeviceCommandQueueResult`, `MirrorLabDeviceLegacyCommand`, `ListLabDeviceCommands`, `GetLabDeviceCommand`. **Anote os nomes/tipos exatos dos campos dos `...Params`/`...Row`** (uuid vira `pgtype.UUID`, `user_id` nulo vira `pgtype.Int4`, `result` nulo vira `pgtype.Text`, timestamps nulos `pgtype.Timestamptz`) — o adaptador do Step 5 usa esses nomes; se o sqlc gerar nome diferente do escrito abaixo, ajuste SÓ o adaptador.

- [ ] **Step 4: Testes do contrato (fake) que falham**

`apps/api-go/hour_lab_commands_test.go`:
```go
package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// fakeLabCmds implementa labCommandStore em memória com a MESMA semântica do
// SQL: fila por device, entrega do mais antigo, uma vez só.
type fakeLabCmds struct {
	mu       sync.Mutex
	pending  map[string][]LabCommand // deviceUUID -> fila
	enqueued []fakeEnqueued
	audits   []fakeEnqueued
	results  map[string]string
	entries  map[string]*LabCommandEntry
	failNext error
}

type fakeEnqueued struct {
	DeviceID, Source, Kind, Text string
	UserID                       int64
}

func newFakeLabCmds() *fakeLabCmds {
	return &fakeLabCmds{pending: map[string][]LabCommand{}, results: map[string]string{}, entries: map[string]*LabCommandEntry{}}
}

func (f *fakeLabCmds) Enqueue(_ context.Context, deviceID string, userID int64, source, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return "", err
	}
	id := fmt.Sprintf("11111111-1111-1111-1111-%012d", len(f.enqueued)+1)
	f.enqueued = append(f.enqueued, fakeEnqueued{deviceID, source, "command", text, userID})
	return id, nil
}

func (f *fakeLabCmds) Audit(_ context.Context, deviceID string, userID int64, source, kind, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, fakeEnqueued{deviceID, source, kind, text, userID})
	return nil
}

func (f *fakeLabCmds) push(deviceUUID string, c LabCommand) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending[deviceUUID] = append(f.pending[deviceUUID], c)
}

func (f *fakeLabCmds) ClaimNext(_ context.Context, deviceUUID string) (*LabCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.pending[deviceUUID]
	if len(q) == 0 {
		return nil, nil
	}
	c := q[0]
	f.pending[deviceUUID] = q[1:]
	return &c, nil
}

func (f *fakeLabCmds) StoreResult(_ context.Context, _, commandID, result string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.results[commandID]; ok {
		return false, nil
	}
	f.results[commandID] = result
	return true, nil
}

func (f *fakeLabCmds) List(_ context.Context, _ string, _ int32) ([]LabCommandEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []LabCommandEntry{}
	for _, e := range f.entries {
		out = append(out, *e)
	}
	return out, nil
}

func (f *fakeLabCmds) Get(_ context.Context, _, commandID string) (*LabCommandEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entries[commandID], nil
}

var _ labCommandStore = (*fakeLabCmds)(nil)
var _ labCommandStore = sqlLabCommandStore{}

func TestFakeClaimNextEntregaUmaVezSo(t *testing.T) {
	f := newFakeLabCmds()
	f.push("pc-1", LabCommand{ID: "a", Text: "hostname"})
	c1, _ := f.ClaimNext(context.Background(), "pc-1")
	c2, _ := f.ClaimNext(context.Background(), "pc-1")
	if c1 == nil || c1.ID != "a" || c2 != nil {
		t.Fatalf("c1=%v c2=%v", c1, c2)
	}
}
```

- [ ] **Step 5: Implementar `hour_lab_commands.go`**

```go
package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"santos-tech/api-go/db"
)

// Fila + auditoria das ações remotas nos PCs (tabela hour_lab_device_commands).
// Entrega at-most-once: o comando roda como SYSTEM no PC, então reentregar
// depois de uma resposta perdida (e reexecutar) é pior que perder — quem
// mandou vê "entregue, sem resultado" no histórico.

type LabCommand struct{ ID, Text string }

type LabCommandEntry struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Source      string     `json:"source"`
	Text        string     `json:"text"`
	Result      *string    `json:"result"`
	UserID      *int64     `json:"userId"`
	UserName    string     `json:"userName"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeliveredAt *time.Time `json:"deliveredAt"`
	ResultAt    *time.Time `json:"resultAt"`
}

type labCommandStore interface {
	Enqueue(ctx context.Context, deviceID string, userID int64, source, text string) (string, error)
	Audit(ctx context.Context, deviceID string, userID int64, source, kind, text string) error
	ClaimNext(ctx context.Context, deviceUUID string) (*LabCommand, error)
	StoreResult(ctx context.Context, deviceUUID, commandID, result string) (bool, error)
	List(ctx context.Context, deviceID string, limit int32) ([]LabCommandEntry, error)
	Get(ctx context.Context, deviceID, commandID string) (*LabCommandEntry, error)
}

type sqlLabCommandStore struct{ q *db.Queries }

func int4(v int64) pgtype.Int4 { return pgtype.Int4{Int32: int32(v), Valid: v > 0} }

func (s sqlLabCommandStore) Enqueue(ctx context.Context, deviceID string, userID int64, source, text string) (string, error) {
	id, err := s.q.EnqueueLabDeviceCommand(ctx, db.EnqueueLabDeviceCommandParams{
		DeviceID: uuidToPg(deviceID), UserID: int4(userID), Source: source, Text: text,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // FK: device não existe
		return "", errLabDeviceNotFound
	}
	if err != nil {
		return "", err
	}
	// Espelho legado (dashboard atual lê command_* da listagem) — best-effort.
	_ = s.q.MirrorLabDeviceLegacyCommand(ctx, db.MirrorLabDeviceLegacyCommandParams{
		CommandID: uuidToPg(id), Text: pgtype.Text{String: text, Valid: true}, DeviceID: uuidToPg(deviceID),
	})
	return id, nil
}

func (s sqlLabCommandStore) Audit(ctx context.Context, deviceID string, userID int64, source, kind, text string) error {
	return s.q.InsertLabDeviceAudit(ctx, db.InsertLabDeviceAuditParams{
		DeviceID: uuidToPg(deviceID), UserID: int4(userID), Source: source, Kind: kind, Text: text,
	})
}

func (s sqlLabCommandStore) ClaimNext(ctx context.Context, deviceUUID string) (*LabCommand, error) {
	row, err := s.q.ClaimNextLabDeviceCommand(ctx, deviceUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &LabCommand{ID: row.ID, Text: row.Text}, nil
}

func (s sqlLabCommandStore) StoreResult(ctx context.Context, deviceUUID, commandID, result string) (bool, error) {
	if !uuidRe.MatchString(commandID) {
		return false, nil
	}
	n, err := s.q.StoreLabDeviceCommandQueueResult(ctx, db.StoreLabDeviceCommandQueueResultParams{
		Result: pgtype.Text{String: result, Valid: true}, DeviceUuid: deviceUUID, CommandID: uuidToPg(commandID),
	})
	return n > 0, err
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func entryFrom(id, kind, source, text string, result pgtype.Text, userID pgtype.Int4, userName string,
	created time.Time, delivered, resultAt pgtype.Timestamptz) LabCommandEntry {
	e := LabCommandEntry{ID: id, Kind: kind, Source: source, Text: text, UserName: userName,
		CreatedAt: created, DeliveredAt: tsPtr(delivered), ResultAt: tsPtr(resultAt)}
	if result.Valid {
		r := result.String
		e.Result = &r
	}
	if userID.Valid {
		u := int64(userID.Int32)
		e.UserID = &u
	}
	return e
}

func (s sqlLabCommandStore) List(ctx context.Context, deviceID string, limit int32) ([]LabCommandEntry, error) {
	rows, err := s.q.ListLabDeviceCommands(ctx, db.ListLabDeviceCommandsParams{DeviceID: uuidToPg(deviceID), Lim: limit})
	if err != nil {
		return nil, err
	}
	out := make([]LabCommandEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, entryFrom(r.ID, r.Kind, r.Source, r.Text, r.Result, r.UserID, r.UserName, r.CreatedAt, r.DeliveredAt, r.ResultAt))
	}
	return out, nil
}

func (s sqlLabCommandStore) Get(ctx context.Context, deviceID, commandID string) (*LabCommandEntry, error) {
	r, err := s.q.GetLabDeviceCommand(ctx, db.GetLabDeviceCommandParams{DeviceID: uuidToPg(deviceID), ID: uuidToPg(commandID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e := entryFrom(r.ID, r.Kind, r.Source, r.Text, r.Result, r.UserID, r.UserName, r.CreatedAt, r.DeliveredAt, r.ResultAt)
	return &e, nil
}
```
O import path do pacote `db` é o que os outros arquivos já usam — copie de `agenda.go` (`grep -n '/db"' apps/api-go/agenda.go`). Se `created_at` vier como `pgtype.Timestamptz` (e não `time.Time`) no Row gerado, use `r.CreatedAt.Time`.

`server.go`: no struct `Server`, acrescentar `labCmds labCommandStore`; em `NewServer` (linha 56), depois de montar `s`: `s.labCmds = sqlLabCommandStore{q: s.q}`.

- [ ] **Step 6: Testes + gate**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin bash -c 'gofmt -l . ; go vet ./... && go build ./... && go test ./...'`
Expected: PASS (`TestFakeClaimNextEntregaUmaVezSo` e as asserções de interface compilam).

- [ ] **Step 7: Commit**

```bash
git add apps/api-go && git commit -m "feat(api-go): fila e auditoria de comandos dos PCs (hour_lab_device_commands)"
```

---

### Task 5: Comandos passam pela fila (envio, entrega, resultado, histórico, auditoria)

**Files:**
- Modify: `apps/api-go/handlers_lab_devices.go` (heartbeat ~linha 188; `handleSendLabDeviceCommand` ~linha 468; `handleLabDeviceCommandResult` linha 226; handlers de lock/restart/shutdown/message)
- Modify: `apps/api-go/handlers_lab_device_shell.go` (`handleLabDeviceShellWS`, auditoria de abertura/fechamento)
- Modify: `apps/api-go/hour_lab_commands.go` (helpers)
- Modify: `apps/api-go/routes.go` (rotas novas de leitura)
- Test: `apps/api-go/hour_lab_commands_test.go`

**Interfaces:**
- Consumes: `labCommandStore`, `fakeLabCmds` (Task 4).
- Produces:
  - `const watchdogAppVersion = "wnsh-watchdog"`
  - `func shouldDeliverCommand(appVersion string, watchdogRecente bool) bool`
  - `func requestTokenSource(r *http.Request, secret string) string`
  - `func (s *Server) nextLabCommandForHeartbeat(ctx context.Context, deviceUUID, appVersion string) *LabCommand`
  - `func (s *Server) auditLabDevice(r *http.Request, deviceID, kind, text string)`
  - `POST /hour-lab-devices/{id}/command` → **201** `{"commandId": "<uuid>"}` (antes 204)
  - `GET /hour-lab-devices/{id}/commands?limit=N` → `{"commands": [LabCommandEntry...]}`
  - `GET /hour-lab-devices/{id}/commands/{cmdId}` → `{"command": LabCommandEntry}` ou 404 `COMMAND_NOT_FOUND`

- [ ] **Step 1: Testes que falham**

Acrescentar em `hour_lab_commands_test.go`:
```go
func TestShouldDeliverCommand(t *testing.T) {
	cases := []struct {
		app  string
		wd   bool
		want bool
	}{
		{"wnsh-watchdog", false, true},
		{"wnsh-watchdog", true, true},
		{"0.1.12", true, false}, // app + watchdog no mesmo PC: só o watchdog executa
		{"0.1.12", false, true}, // PC só com o app continua recebendo
	}
	for _, c := range cases {
		if got := shouldDeliverCommand(c.app, c.wd); got != c.want {
			t.Errorf("app=%s wd=%v: got %v", c.app, c.wd, got)
		}
	}
}

func TestRequestTokenSource(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer st_abc")
	if got := requestTokenSource(r, "x"); got != "pat" {
		t.Fatalf("PAT: %s", got)
	}
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(&http.Cookie{Name: "access_token", Value: "qualquer"})
	if got := requestTokenSource(r2, "x"); got != "painel" {
		t.Fatalf("cookie: %s", got)
	}
}

func TestHeartbeatSoEntregaProWatchdogQuandoHaWatchdog(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	f := newFakeLabCmds()
	s.labCmds = f
	f.push("pc-1", LabCommand{ID: "a", Text: "hostname"})
	// watchdog bate primeiro: marca presença e leva o comando
	if c := s.nextLabCommandForHeartbeat(context.Background(), "pc-1", watchdogAppVersion); c == nil || c.ID != "a" {
		t.Fatalf("watchdog deveria receber: %v", c)
	}
	f.push("pc-1", LabCommand{ID: "b", Text: "whoami"})
	// app do usuário no mesmo PC: não recebe
	if c := s.nextLabCommandForHeartbeat(context.Background(), "pc-1", "0.1.12"); c != nil {
		t.Fatalf("app não deveria receber com watchdog presente: %v", c)
	}
	// e o comando continua lá pro watchdog
	if c := s.nextLabCommandForHeartbeat(context.Background(), "pc-1", watchdogAppVersion); c == nil || c.ID != "b" {
		t.Fatalf("watchdog deveria receber b: %v", c)
	}
}

func TestSendCommandEnfileiraComUsuarioEOrigem(t *testing.T) {
	s := testServer(Config{JWTSecret: "x"})
	f := newFakeLabCmds()
	s.labCmds = f
	id := "2a502d12-676b-4723-80ae-b65335514df9"
	r := httptest.NewRequest("POST", "/hour-lab-devices/"+id+"/command", strings.NewReader(`{"text":"hostname"}`))
	r.SetPathValue("id", id)
	r.Header.Set("Authorization", "Bearer st_pat")
	w := httptest.NewRecorder()
	s.handleSendLabDeviceCommand(w, reqAs(r, 55))
	if w.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
	if len(f.enqueued) != 1 || f.enqueued[0].UserID != 55 || f.enqueued[0].Source != "pat" || f.enqueued[0].DeviceID != id {
		t.Fatalf("enfileirado errado: %+v", f.enqueued)
	}
	var out struct{ CommandID string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.CommandID == "" {
		t.Fatalf("sem commandId: %s", w.Body)
	}
}

func TestSendCommandDeviceInexistente404(t *testing.T) {
	s := testServer(Config{})
	f := newFakeLabCmds()
	f.failNext = errLabDeviceNotFound
	s.labCmds = f
	id := "2a502d12-676b-4723-80ae-b65335514df9"
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"x"}`))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	s.handleSendLabDeviceCommand(w, reqAs(r, 55))
	if w.Code != http.StatusNotFound {
		t.Fatalf("code=%d", w.Code)
	}
}
```
(acrescentar imports `encoding/json`, `net/http`, `net/http/httptest`, `strings`).

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin go test -run 'TestShouldDeliver|TestRequestTokenSource|TestHeartbeatSoEntrega|TestSendCommand' ./...`
Expected: FAIL — símbolos indefinidos; `handleSendLabDeviceCommand` devolve 204.

- [ ] **Step 3: Helpers em `hour_lab_commands.go`**

```go
const watchdogAppVersion = "wnsh-watchdog"

// Presença do watchdog por PC (Redis, 5min). Com ele presente, o hour-timer-app
// (que roda como o usuário logado) NÃO recebe comando — os dois executavam o
// mesmo comando e o do usuário deixou arquivo com dono errado (gazake, 25/09).
const labWatchdogKeyPrefix = "api-go:lab:wd:"
const labWatchdogTTL = 5 * time.Minute

func shouldDeliverCommand(appVersion string, watchdogRecente bool) bool {
	if appVersion == watchdogAppVersion {
		return true
	}
	return !watchdogRecente
}

func (s *Server) markWatchdogSeen(ctx context.Context, deviceUUID string) {
	if s.rdb == nil {
		return
	}
	_ = s.rdb.Set(ctx, labWatchdogKeyPrefix+deviceUUID, 1, labWatchdogTTL).Err()
}

// watchdogSeenRecently: erro de Redis conta como "presente" — na dúvida o app
// não executa (o watchdog pega no próximo ciclo).
func (s *Server) watchdogSeenRecently(ctx context.Context, deviceUUID string) bool {
	if s.rdb == nil {
		return false
	}
	n, err := s.rdb.Exists(ctx, labWatchdogKeyPrefix+deviceUUID).Result()
	return err != nil || n > 0
}

func (s *Server) nextLabCommandForHeartbeat(ctx context.Context, deviceUUID, appVersion string) *LabCommand {
	if s.labCmds == nil {
		return nil
	}
	isWD := appVersion == watchdogAppVersion
	if isWD {
		s.markWatchdogSeen(ctx, deviceUUID)
	}
	if !shouldDeliverCommand(appVersion, !isWD && s.watchdogSeenRecently(ctx, deviceUUID)) {
		return nil
	}
	cmd, err := s.labCmds.ClaimNext(ctx, deviceUUID)
	if err != nil {
		slog.Warn("fila de comandos: falha ao entregar", "device", deviceUUID, "err", err)
		return nil
	}
	return cmd
}

// requestTokenSource classifica a origem do request pra auditoria.
func requestTokenSource(r *http.Request, secret string) string {
	if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
		if tokenAudience(c.Value, secret) != "" {
			return "mcp_oauth"
		}
		return "painel"
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case strings.HasPrefix(tok, "st_"):
		return "pat"
	case tok != "" && tokenAudience(tok, secret) != "":
		return "mcp_oauth"
	default:
		return "api"
	}
}

// auditLabDevice grava a trilha (best-effort: auditoria falhar não pode
// impedir o admin de travar um PC).
func (s *Server) auditLabDevice(r *http.Request, deviceID, kind, text string) {
	if s.labCmds == nil {
		return
	}
	if err := s.labCmds.Audit(r.Context(), deviceID, userIDFrom(r), requestTokenSource(r, s.cfg.JWTSecret), kind, text); err != nil {
		slog.Warn("auditoria de dispositivo falhou", "device", deviceID, "kind", kind, "err", err)
	}
}
```
(imports: `log/slog`, `net/http`, `strings`).

- [ ] **Step 4: Handlers**

Heartbeat (`handlers_lab_devices.go`, bloco `if res.CommandID != nil {...}` ~linha 188) vira:
```go
	// Comando sai da FILA (hour_lab_device_commands), não mais da coluna
	// command_* — que ficou só como espelho pro dashboard atual.
	if cmd := s.nextLabCommandForHeartbeat(r.Context(), in.DeviceID, in.AppVersion); cmd != nil {
		resp["command"] = map[string]any{"id": cmd.ID, "text": cmd.Text}
	}
```
`handleSendLabDeviceCommand`: trocar `s.sendLabDeviceCommand(...)` + `WriteHeader(204)` por:
```go
	cmdID, err := s.labCmds.Enqueue(r.Context(), id, userIDFrom(r), requestTokenSource(r, s.cfg.JWTSecret), in.Text)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"commandId": cmdID})
```
e apagar `sendLabDeviceCommand` de `hour_lab_devices.go` (sem uso). Ajustar o comentário do handler (resultado agora em `GET /hour-lab-devices/{id}/commands/{cmdId}`).
`handleLabDeviceCommandResult`: depois do `s.storeLabDeviceCommandResult(...)` que já autentica e grava o espelho legado, acrescentar:
```go
	if s.labCmds != nil {
		if _, err := s.labCmds.StoreResult(r.Context(), in.DeviceID, in.CommandID, in.Result); err != nil {
			writeErr(w, err)
			return
		}
	}
```
(usar os nomes de campo que o handler já decodifica — conferir o struct `in` dele).
Auditoria: nos handlers `handleLockLabDevice`, `handleRestartLabDevice`, `handleShutdownLabDevice`, `handleSendLabDeviceMessage`, logo antes do `w.WriteHeader(http.StatusNoContent)` de sucesso: `s.auditLabDevice(r, id, "lock", "")` / `"restart"` / `"shutdown"` / `"message", in.Text`. Em `handleLabDeviceShellWS`, após o relay começar: `s.auditLabDevice(r, id, "shell_open", "")`, e num `defer` do mesmo escopo: `s.auditLabDevice(r, id, "shell_close", "")` (use `context.WithoutCancel(r.Context())` se o ctx já estiver cancelado no fechamento — crie a cópia `r2 := r.WithContext(context.WithoutCancel(r.Context()))` e passe `r2`).

Leitura (novos handlers em `hour_lab_commands.go`):
```go
// GET /hour-lab-devices/{id}/commands?limit=50
func (s *Server) handleListLabDeviceCommands(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := int32(50)
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = int32(v)
	}
	list, err := s.labCmds.List(r.Context(), id, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": list})
}

var errLabCommandNotFound = appErr(http.StatusNotFound, "COMMAND_NOT_FOUND", "Comando não encontrado")

// GET /hour-lab-devices/{id}/commands/{cmdId}
func (s *Server) handleGetLabDeviceCommand(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	cmdID, err := hourUUIDFrom(r, "cmdId", errLabCommandNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	e, err := s.labCmds.Get(r.Context(), id, cmdID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if e == nil {
		writeErr(w, errLabCommandNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"command": e})
}
```
`routes.go`, dentro de `registerLabDeviceRoutes` (guard provisório `adminGuard`; a Task 7 troca todos):
```go
	mux.HandleFunc("GET /hour-lab-devices/{id}/commands", s.adminGuard(s.handleListLabDeviceCommands))
	mux.HandleFunc("GET /hour-lab-devices/{id}/commands/{cmdId}", s.adminGuard(s.handleGetLabDeviceCommand))
```

- [ ] **Step 5: Testes + gate**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin bash -c 'gofmt -l . ; go vet ./... && go build ./... && go test ./...'`
Expected: PASS, inclusive `TestRoutesRegisterWithoutPanic` (padrão `{id}/commands/{cmdId}` não conflita). Os testes antigos de heartbeat (`TestHeartbeatEntregaComandosUmaVezSo` etc.) continuam passando porque testam `upsertLabDeviceHeartbeatTx`, que não mudou.

- [ ] **Step 6: Commit**

```bash
git add apps/api-go && git commit -m "feat(api-go): comandos dos PCs pela fila, só pro watchdog, com histórico e auditoria"
```

---

### Task 6: Long-poll `POST /public/lab-devices/wait-command`

**Files:**
- Modify: `apps/api-go/hour_lab_commands.go`
- Modify: `apps/api-go/hour_lab_devices.go` (helper de auth sem adoção)
- Modify: `apps/api-go/routes.go` (bloco `/public/lab-devices/*`, ~linha 505)
- Test: `apps/api-go/hour_lab_commands_test.go`

**Interfaces:**
- Consumes: `labCommandStore.ClaimNext`, `authLabDeviceTx` (existente, `hour_lab_devices.go:417`), `markWatchdogSeen` (Task 5).
- Produces:
  - `var labWaitCommandMax = 50 * time.Second; var labWaitCommandTick = time.Second`
  - `func waitForLabCommand(ctx context.Context, store labCommandStore, deviceUUID string, max, tick time.Duration) (*LabCommand, error)`
  - `func (s *Server) authLabDeviceNoAdopt(ctx context.Context, deviceUUID, deviceSecret string) error`
  - Rota: `POST /public/lab-devices/wait-command {deviceId, deviceSecret}` → 200 `{"command":{"id","text"}}` | 204 | 401.

- [ ] **Step 1: Testes que falham**

```go
func TestWaitForLabCommandVoltaNaHora(t *testing.T) {
	f := newFakeLabCmds()
	f.push("pc-1", LabCommand{ID: "a", Text: "x"})
	start := time.Now()
	c, err := waitForLabCommand(context.Background(), f, "pc-1", time.Second, 10*time.Millisecond)
	if err != nil || c == nil || c.ID != "a" {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("deveria responder sem esperar o tick")
	}
}

func TestWaitForLabCommandPegaOQueChegaDurante(t *testing.T) {
	f := newFakeLabCmds()
	go func() { time.Sleep(30 * time.Millisecond); f.push("pc-1", LabCommand{ID: "b", Text: "y"}) }()
	c, _ := waitForLabCommand(context.Background(), f, "pc-1", time.Second, 10*time.Millisecond)
	if c == nil || c.ID != "b" {
		t.Fatalf("c=%v", c)
	}
}

func TestWaitForLabCommandEntregaEmOrdem(t *testing.T) {
	f := newFakeLabCmds()
	f.push("pc-1", LabCommand{ID: "primeiro", Text: "1"})
	f.push("pc-1", LabCommand{ID: "segundo", Text: "2"})
	c1, _ := waitForLabCommand(context.Background(), f, "pc-1", time.Second, 10*time.Millisecond)
	c2, _ := waitForLabCommand(context.Background(), f, "pc-1", time.Second, 10*time.Millisecond)
	if c1.ID != "primeiro" || c2.ID != "segundo" {
		t.Fatalf("ordem errada: %s, %s", c1.ID, c2.ID)
	}
}

func TestWaitForLabCommandTimeoutDevolveNil(t *testing.T) {
	c, err := waitForLabCommand(context.Background(), newFakeLabCmds(), "pc-1", 40*time.Millisecond, 10*time.Millisecond)
	if c != nil || err != nil {
		t.Fatalf("c=%v err=%v", c, err)
	}
}

func TestWaitForLabCommandRespeitaCancelamento(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	_, _ = waitForLabCommand(ctx, newFakeLabCmds(), "pc-1", 5*time.Second, 10*time.Millisecond)
	if time.Since(start) > time.Second {
		t.Fatal("não parou ao cancelar (cliente desconectou)")
	}
}

func TestWaitCommandCorpoInvalido400(t *testing.T) {
	s := testServer(Config{})
	s.labCmds = newFakeLabCmds()
	w := httptest.NewRecorder()
	s.handleLabDeviceWaitCommand(w, httptest.NewRequest("POST", "/", strings.NewReader(`{`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", w.Code)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin go test -run 'TestWaitFor|TestWaitCommand' ./...`
Expected: FAIL — `undefined: waitForLabCommand`.

- [ ] **Step 3: Implementar**

`hour_lab_commands.go`:
```go
// Long-poll: o watchdog segura esta requisição em vez de dormir 60s; o comando
// sai em ~1s. Estado no banco (não em memória) → funciona com N réplicas.
// labWaitCommandMax fica abaixo do WriteTimeout (60s, main.go).
var (
	labWaitCommandMax  = 50 * time.Second
	labWaitCommandTick = time.Second
)

func waitForLabCommand(ctx context.Context, store labCommandStore, deviceUUID string, max, tick time.Duration) (*LabCommand, error) {
	deadline := time.Now().Add(max)
	for {
		cmd, err := store.ClaimNext(ctx, deviceUUID)
		if err != nil || cmd != nil {
			return cmd, err
		}
		if time.Now().Add(tick).After(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(tick):
		}
	}
}

// POST /public/lab-devices/wait-command — {deviceId, deviceSecret}
func (s *Server) handleLabDeviceWaitCommand(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		DeviceID     string `json:"deviceId"`
		DeviceSecret string `json:"deviceSecret"`
	}
	if err := decodeJSON(r, &in); err != nil || in.DeviceID == "" || len(in.DeviceID) > 100 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := s.authLabDeviceNoAdopt(r.Context(), in.DeviceID, in.DeviceSecret); err != nil {
		writeErr(w, err)
		return
	}
	s.markWatchdogSeen(r.Context(), in.DeviceID) // só o watchdog chama esta rota
	cmd, err := waitForLabCommand(r.Context(), s.labCmds, in.DeviceID, labWaitCommandMax, labWaitCommandTick)
	if err != nil {
		writeErr(w, err)
		return
	}
	if cmd == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"command": map[string]any{"id": cmd.ID, "text": cmd.Text}})
}
```
`hour_lab_devices.go`, perto de `authLabDeviceTx`:
```go
// authLabDeviceNoAdopt autentica o PC SEM adotar: rotas secundárias (long-poll)
// não podem criar segredo — isso é só do heartbeat. Se o authLabDeviceTx
// "cunharia" um segredo, a transação é desfeita e o PC recebe 401.
func (s *Server) authLabDeviceNoAdopt(ctx context.Context, deviceUUID, deviceSecret string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // só leitura na prática
	minted, err := authLabDeviceTx(ctx, tx, deviceUUID, deviceSecret)
	if err != nil {
		return err
	}
	if minted != nil {
		return errLabDeviceUnauthorized
	}
	return nil
}
```
`routes.go`, junto das rotas `/public/lab-devices/*`:
```go
	// Long-poll do watchdog: segura até 50s e entrega o próximo comando na hora.
	mux.HandleFunc("POST /public/lab-devices/wait-command", s.rateLimit(120, min, s.handleLabDeviceWaitCommand))
```

- [ ] **Step 4: Testes + gate**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin bash -c 'gofmt -l . ; go vet ./... && go build ./... && go test ./...'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/api-go && git commit -m "feat(api-go): long-poll de comandos pro watchdog (wait-command)"
```

---

### Task 7: Rotas de dispositivos por permissão

**Files:**
- Modify: `apps/api-go/routes.go` (`registerLabDeviceRoutes`, linhas 454-511)
- Test: `apps/api-go/hour_lab_commands_test.go`

**Interfaces:**
- Consumes: `permGuard` com `hasPerm` (Task 2); handlers das Tasks 5–6.
- Produces: helper `func (s *Server) devPerm(action string, h http.HandlerFunc) http.HandlerFunc` = `s.permGuard("dispositivos", action, false, h)`.

- [ ] **Step 1: Teste que falha**

```go
func TestDispositivosExecutarNegaSemPermissao(t *testing.T) {
	s := testServerWithRedis(t, Config{JWTSecret: "x"})
	// usuário 55 só com dispositivos:ver, direto no cache (sem Postgres)
	u := &User{ID: 55, Role: RoleCustom, Permissions: map[string][]string{"dispositivos": {"ver"}}}
	b, _ := json.Marshal(u)
	if err := s.rdb.Set(context.Background(), cacheUserKey(55), b, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	f := newFakeLabCmds()
	s.labCmds = f
	chamou := false
	h := s.devPerm("executar", func(http.ResponseWriter, *http.Request) { chamou = true })
	tok := mustAccessToken(t, s, 55)
	r := httptest.NewRequest("POST", "/hour-lab-devices/x/command", strings.NewReader(`{"text":"x"}`))
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusForbidden || chamou {
		t.Fatalf("code=%d chamou=%v", w.Code, chamou)
	}
	hv := s.devPerm("ver", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/hour-lab-devices", nil)
	r2.Header.Set("Authorization", "Bearer "+tok)
	hv(w2, r2)
	if w2.Code != 204 {
		t.Fatalf("ver deveria passar: %d %s", w2.Code, w2.Body)
	}
}
```
Helper no mesmo arquivo (usa `generateTokens`, `token.go:27`):
```go
func mustAccessToken(t *testing.T, s *Server, uid int64) string {
	t.Helper()
	s.cfg.JWTRefreshSecret = "x-refresh" // generateTokens exige os dois segredos
	access, _, err := generateTokens(s.cfg.JWTSecret, s.cfg.JWTRefreshSecret, uid, "biel@santos-tech.com", "Gabriel")
	if err != nil {
		t.Fatal(err)
	}
	return access
}
```
(confira o nome do campo do refresh secret em `Config` — `grep -n "JWTRefresh" apps/api-go/config.go`). Se o `authGuard` fizer alguma checagem extra que o usuário do cache não passe (ex.: revogação por timestamp no Redis), ajuste o `User`/Redis do teste pra satisfazê-la — o objetivo do teste é só o `permGuard`.

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin go test -run TestDispositivosExecutarNegaSemPermissao ./...`
Expected: FAIL — `s.devPerm undefined`.

- [ ] **Step 3: Implementar**

Em `routes.go`, antes de `registerLabDeviceRoutes`:
```go
// devPerm: guard das rotas de dispositivos — admin sempre; demais só com
// dispositivos:<ação> (individual ou do cargo). Ao contrário do adminGuard,
// aceita token OAuth: é o que deixa o conector MCP do claude.ai operar a frota
// (decisão de 25/09/2026, spec acesso-remoto-frota §2).
func (s *Server) devPerm(action string, h http.HandlerFunc) http.HandlerFunc {
	return s.permGuard("dispositivos", action, false, h)
}
```
Dentro de `registerLabDeviceRoutes`, trocar `s.adminGuard(` por `s.devPerm("<ação>", ` conforme a tabela (mantendo `rateLimit` e `sudoGuard` onde já existem):

| Rota | Ação |
|---|---|
| `GET /hour-lab-devices`, `GET /{id}/programs`, `GET /{id}/screenshots`, `GET /hour-lab-devices/program-icons/{hash}` (conferir o padrão exato no arquivo), `GET /{id}/commands`, `GET /{id}/commands/{cmdId}` | `ver` |
| `POST /{id}/message`, `/lock`, `/restart`, `/shutdown`, `POST /{id}/screenshot`, `DELETE /{id}/screenshots/{shotId}` | `controlar` |
| `POST /{id}/command`, `GET /{id}/shell`, `GET /{id}/shell-check` (os dois últimos continuam `s.devPerm("executar", s.sudoGuard(...))`) | `executar` |
| `POST /pair`, `PATCH /{id}`, `DELETE /{id}`, `/unpair`, `/reset-secret`, todas as `/hour-lab-expected-programs` | `gerenciar` |

As 6 rotas `/public/lab-devices/*` não mudam. Conferir no fim: `grep -n "adminGuard" apps/api-go/routes.go | sed -n '/lab/p'` → nenhuma linha de dispositivo com `adminGuard`.

- [ ] **Step 4: Testes + gate**

Run: `cd apps/api-go && PATH=$PATH:$HOME/.local/bin bash -c 'gofmt -l . ; go vet ./... && go build ./... && go test ./...'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/api-go && git commit -m "feat(api-go): rotas de dispositivos por permissão (dispositivos:ver/controlar/executar/gerenciar)"
```

---

### Task 8: Documentação dos contratos (OpenAPI + llms.txt)

**Files:**
- Modify: `docs/openapi.yaml`
- Modify: `apps/api-go/llms.txt` (seção de lab devices, ~linhas 446-522 e 591-600)

- [ ] **Step 1: OpenAPI**

Acrescentar/alterar: `PATCH /auth/admin/users/{id}` (campo `permissions`, 403 `SUDO_REQUIRED`, `customRoleId` opcional com role=4); `/auth/me` (`permissions` efetivas p/ qualquer role≠3, `userPermissions`); `POST /hour-lab-devices/{id}/command` (201 `{commandId}`); `GET /hour-lab-devices/{id}/commands`, `GET /hour-lab-devices/{id}/commands/{cmdId}` (schema `LabCommandEntry` com os campos da Task 4); `POST /public/lab-devices/wait-command` (200/204/401); nota de segurança de cada rota de dispositivo com a permissão exigida.

- [ ] **Step 2: llms.txt**

Na seção de dispositivos: tabela ação→rotas da Task 7; fila at-most-once; long-poll; que `command` só vai pro watchdog quando ele existe; e a nova seção "Permissões individuais" (formato, regra efetiva, edição exige sudo).

- [ ] **Step 3: CLAUDE.md**

Em `CLAUDE.md` (raiz), trocar o parágrafo "**[PLANEJADO, não implementado ainda]** Provisionamento automático de chave SSH ..." por um resumo do que existe: o app gera a chave do PC e manda em `sshPublicKey`; o acesso de admin é pela conta `santos-fleet` + `administrators_authorized_keys` (ver `infra/fleet/README.md`).

- [ ] **Step 4: Validar YAML e commit**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('docs/openapi.yaml'))" && echo OK`
Expected: `OK`.
```bash
git add docs/openapi.yaml apps/api-go/llms.txt CLAUDE.md && git commit -m "docs(api-go): permissões individuais, fila de comandos e wait-command"
```

---

### Task 9: Watchdog — long-poll, fix do segredo, canário e publicação

**Files:**
- Modify: `infra/fleet/wnsh-loop.ps1`

**Interfaces:**
- Consumes: `POST /public/lab-devices/wait-command` (Task 6, precisa estar EM PRODUÇÃO antes do Step 5); `infra/fleet/check-ps1.sh` (Task 1).
- Produces: função PowerShell `Invoke-SantosCommand($cmd, $machineGuid, $deviceSecret)`.

- [ ] **Step 1: Extrair a execução de comando pra função**

No topo do arquivo (depois de `Sync-SantosProgramInventory`), a função — mesmo corpo do bloco atual das linhas ~371-396 (Start-Job, 60s, 8000 chars, POST command-result):
```powershell
# Executa um comando da fila e devolve o resultado. Usado pelo heartbeat E
# pelo long-poll (wait-command). Grava o id ANTES de executar (ver comentário
# do $lastCommandIdFile): se o comando matar o processo, não repete.
function Invoke-SantosCommand($cmd, $machineGuid, $deviceSecret) {
    if (-not $cmd -or -not $cmd.id -or $cmd.id -eq $script:lastCommandId) { return }
    $script:lastCommandId = $cmd.id
    try { Set-Content -Path $script:lastCommandIdFile -Value $cmd.id -NoNewline -Encoding ascii -ErrorAction SilentlyContinue } catch {}
    try {
        $cmdJob = Start-Job -ScriptBlock {
            param($cmdText)
            try { Invoke-Expression $cmdText 2>&1 | Out-String } catch { "ERRO: $($_.Exception.Message)" }
        } -ArgumentList $cmd.text
        if (Wait-Job $cmdJob -Timeout 60) { $cmdOutput = Receive-Job $cmdJob } else { Stop-Job $cmdJob; $cmdOutput = "(comando nao terminou em 60s, cancelado)" }
        Remove-Job $cmdJob -Force -ErrorAction SilentlyContinue
        if (-not $cmdOutput) { $cmdOutput = "(sem saida)" }
        $cmdOutput = [string]$cmdOutput
        if ($cmdOutput.Length -gt 8000) { $cmdOutput = $cmdOutput.Substring(0, 8000) }
        $resultBody = @{ deviceId = $machineGuid; deviceSecret = $deviceSecret; commandId = $cmd.id; result = $cmdOutput } | ConvertTo-Json -Compress -Depth 4
        Invoke-RestMethod -Uri "https://api.santos-tech.com/public/lab-devices/command-result" -Method Post -Body ([System.Text.Encoding]::UTF8.GetBytes($resultBody)) -ContentType "application/json; charset=utf-8" -TimeoutSec 30 *> $null
    } catch {}
}
```
Trocar `$lastCommandIdFile = ...` e `$lastCommandId = ...` (antes do `while`) por `$script:lastCommandIdFile` / `$script:lastCommandId` (mesmos valores). Substituir o bloco inteiro `if ($resp.command -and ...) { ... }` por:
```powershell
        Invoke-SantosCommand -cmd $resp.command -machineGuid $machineGuid -deviceSecret $deviceSecret
```

- [ ] **Step 2: Fix do segredo adotado**

Logo depois de `if ($resp.deviceSecret) { Set-Content ... }`:
```powershell
        # 25/09/2026: na volta em que ADOTA um segredo novo, $deviceSecret
        # ainda era o antigo (vazio) e o command-result dessa volta dava 401
        # -- o resultado se perdia (visto no gazake).
        if ($resp.deviceSecret) { $deviceSecret = [string]$resp.deviceSecret }
```

- [ ] **Step 3: Long-poll no lugar do sleep**

Trocar o final do loop (`$iteration++` / `Start-Sleep -Seconds 60`) por:
```powershell
    $iteration++
    # 25/09/2026: em vez de dormir 60s, segura um long-poll na API (ate 50s
    # por chamada) -- comando chega em ~1s. Se a rota falhar (API antiga,
    # rede), cai no sleep do tempo que falta, igual antes.
    $loopUntil = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $loopUntil) {
        $left = [int]($loopUntil - (Get-Date)).TotalSeconds
        if ($left -lt 5) { Start-Sleep -Seconds ([Math]::Max(1, $left)); break }
        try {
            $waitBody = @{ deviceId = $machineGuid; deviceSecret = $deviceSecret } | ConvertTo-Json -Compress
            $w = Invoke-WebRequest -UseBasicParsing -Uri "https://api.santos-tech.com/public/lab-devices/wait-command" -Method Post -Body ([System.Text.Encoding]::UTF8.GetBytes($waitBody)) -ContentType "application/json; charset=utf-8" -TimeoutSec 58
            if ($w.StatusCode -eq 200 -and $w.Content) {
                $wc = $w.Content | ConvertFrom-Json
                Invoke-SantosCommand -cmd $wc.command -machineGuid $machineGuid -deviceSecret $deviceSecret
            }
        } catch {
            Start-Sleep -Seconds ([Math]::Max(1, [int]($loopUntil - (Get-Date)).TotalSeconds))
            break
        }
    }
```
Atenção: `$machineGuid`/`$deviceSecret` são definidos dentro do `try` do heartbeat — declarar `$machineGuid = $null; $deviceSecret = ""` antes do `while ($true)` e só rodar o long-poll se `$machineGuid` não for nulo (`if ($machineGuid) { ...loop acima... } else { Start-Sleep -Seconds 60 }`).

- [ ] **Step 4: Validar sintaxe**

Run: `infra/fleet/check-ps1.sh infra/fleet/wnsh-loop.ps1`
Expected: `OK wnsh-loop.ps1`. Commit:
```bash
git add infra/fleet/wnsh-loop.ps1 && git commit -m "feat(fleet): watchdog usa long-poll pra comandos e corrige segredo na adoção"
```

- [ ] **Step 5: Canário no gazake-1 (só depois do deploy da api-go — Task 11 Step 2)**

```bash
cd ~/Projetos/santos-tech-infra
sed 's#downloads/wnsh-loop.ps1#downloads/wnsh-loop-canary.ps1#' infra/fleet/wnsh-loop.ps1 > /tmp/claude-1000/wnsh-loop-canary.ps1
infra/fleet/check-ps1.sh /tmp/claude-1000/wnsh-loop-canary.ps1
# upload na chave canary (token: memória reference_cloudflare_r2_santos_tech)
curl -fsS -X PUT -H "Authorization: Bearer $CF_R2_TOKEN" -H "Content-Type: text/plain" --data-binary @/tmp/claude-1000/wnsh-loop-canary.ps1 \
  "https://api.cloudflare.com/client/v4/accounts/ac17de2ec0cc4e48dc21ddf2eeb5a879/r2/buckets/santos-tech/objects/downloads/wnsh-loop-canary.ps1"
scp -i ~/.ssh/id_ed25519_fleet_admin /tmp/claude-1000/wnsh-loop-canary.ps1 "santos-fleet@100.118.245.16:C:/ProgramData/SantosTech/wnsh-loop.ps1"
ssh -i ~/.ssh/id_ed25519_fleet_admin santos-fleet@100.118.245.16 "powershell -NoProfile -Command Restart-Service WinNetSvcHost -Force"
```
Validar: enfileirar `hostname` no gazake (`POST /hour-lab-devices/d12bdf46-c5a7-4e36-a477-cec91cef0509/command` com PAT admin, ou INSERT via psql — ver memória `reference_frota_comando_remoto_via_db`) e medir o tempo até `GET .../commands/{id}` trazer `result`. Expected: resultado `GAZAKE` em **< 10s**; `last_seen_at` do gazake continua avançando (heartbeat não quebrou).

- [ ] **Step 6: Publicar pra frota**

```bash
curl -fsS -X PUT -H "Authorization: Bearer $CF_R2_TOKEN" -H "Content-Type: text/plain" --data-binary @infra/fleet/wnsh-loop.ps1 \
  "https://api.cloudflare.com/client/v4/accounts/ac17de2ec0cc4e48dc21ddf2eeb5a879/r2/buckets/santos-tech/objects/downloads/wnsh-loop.ps1"
curl -fsS -X PUT -H "Authorization: Bearer $CF_R2_TOKEN" -H "Content-Type: text/plain" --data-binary @infra/fleet/wnsh-loop.ps1 \
  "https://api.cloudflare.com/client/v4/accounts/ac17de2ec0cc4e48dc21ddf2eeb5a879/r2/buckets/santos-tech/objects/downloads/wnsh-loop-canary.ps1"
curl -s https://cdn.santos-tech.com/downloads/wnsh-loop.ps1 | sha256sum; sha256sum infra/fleet/wnsh-loop.ps1   # iguais
```
Expected: após ~2 min, todos os PCs online com `last_seen_at` recente (`SELECT name, now()-last_seen_at FROM hour_lab_devices ORDER BY 2`) e um `hostname` enfileirado em cada um volta em < 10s. **Se algum PC parar de bater: republicar a versão anterior (`git show HEAD~1:infra/fleet/wnsh-loop.ps1`) na mesma chave imediatamente.**

---

### Task 10: Tools de dispositivos no MCP

**Files:**
- Create: `apps/mcp-go/tools_devices.go`
- Create: `apps/mcp-go/tools_devices_test.go`
- Modify: `apps/mcp-go/server.go` (`MCP()`, linha ~100)

**Interfaces:**
- Consumes: rotas das Tasks 5 e 7; `s.proxy`, `s.proxyUntrusted`, `s.client.do`, `authorization(req.Extra)`, `resultFromMode` (existentes em `server.go`).
- Produces: tools `devices_list`, `device_commands`, `device_control`, `device_command_run`, `device_command_result`.

- [ ] **Step 1: Testes que falham**

`apps/mcp-go/tools_devices_test.go`:
```go
package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const devID = "2a502d12-676b-4723-80ae-b65335514df9"

func TestDeviceCommandRunEnfileiraEEsperaResultado(t *testing.T) {
	var polls atomic.Int32
	var gotBody, gotAuth string
	api := httptest.NewServer(authMeOK(4, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/hour-lab-devices/"+devID+"/command":
			b, _ := io.ReadAll(r.Body)
			gotBody, gotAuth = string(b), r.Header.Get("Authorization")
			w.WriteHeader(201)
			w.Write([]byte(`{"commandId":"c0ffee00-0000-0000-0000-000000000001"}`))
		case r.Method == "GET" && r.URL.Path == "/hour-lab-devices/"+devID+"/commands/c0ffee00-0000-0000-0000-000000000001":
			if polls.Add(1) < 2 {
				w.Write([]byte(`{"command":{"id":"c0ffee00-0000-0000-0000-000000000001","result":null}}`))
				return
			}
			w.Write([]byte(`{"command":{"id":"c0ffee00-0000-0000-0000-000000000001","result":"GAZAKE\r\n"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	devicePollInterval = 10 * time.Millisecond

	session := newTestSession(t, Config{AuthBaseURL: api.URL}, nil, "Bearer st_biel")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "device_command_run", Arguments: map[string]any{"id": devID, "powershell": "hostname"},
	})
	if err != nil {
		t.Fatal(err)
	}
	txt := toolText(t, res)
	if res.IsError || !strings.Contains(txt, "GAZAKE") {
		t.Fatalf("esperava o resultado: %s", txt)
	}
	if gotAuth != "Bearer st_biel" || !strings.Contains(gotBody, `"text":"hostname"`) {
		t.Fatalf("auth=%q body=%q", gotAuth, gotBody)
	}
}

func TestDeviceCommandRunSemPermissaoViraErro(t *testing.T) {
	api := httptest.NewServer(authMeOK(4, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"code":"FORBIDDEN","message":"Acesso restrito"}`))
	}))
	defer api.Close()
	session := newTestSession(t, Config{AuthBaseURL: api.URL}, nil, "Bearer st_x")
	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "device_command_run", Arguments: map[string]any{"id": devID, "powershell": "hostname"},
	})
	if !res.IsError || !strings.Contains(toolText(t, res), "403") {
		t.Fatalf("esperava erro 403: %s", toolText(t, res))
	}
}

func TestDeviceControlValidaAcao(t *testing.T) {
	session := newTestSession(t, Config{AuthBaseURL: newAuthMeServer(t, 3).URL}, nil, "Bearer st_x")
	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "device_control", Arguments: map[string]any{"id": devID, "action": "formatar"},
	})
	if !res.IsError {
		t.Fatal("ação inválida deveria falhar localmente")
	}
}

func TestDevicesListRepassaToken(t *testing.T) {
	var gotURL string
	api := httptest.NewServer(authMeOK(4, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		w.Write([]byte(`{"devices":[]}`))
	}))
	defer api.Close()
	session := newTestSession(t, Config{AuthBaseURL: api.URL}, nil, "Bearer st_x")
	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "devices_list", Arguments: map[string]any{}})
	if res.IsError || gotURL != "/hour-lab-devices" {
		t.Fatalf("url=%q err=%s", gotURL, toolText(t, res))
	}
}
```
(acrescentar `"time"` aos imports).

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/mcp-go && PATH=$PATH:$HOME/.local/bin go test -run 'TestDevice' ./...`
Expected: FAIL — tool `device_command_run` inexistente / `devicePollInterval` indefinido.

- [ ] **Step 3: Implementar `tools_devices.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools dos PCs do laboratório (api-go /hour-lab-devices). Permissão é da API
// (permGuard dispositivos:ver/controlar/executar) — aqui só repassamos o token
// do usuário. Saídas de PC (títulos de janela, stdout de comando) são texto de
// terceiros → proxyUntrusted.

var deviceIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

// devicePollInterval/deviceRunWait: variáveis pra os testes encurtarem.
var (
	devicePollInterval = time.Second
	deviceRunWait      = 45 * time.Second
)

type deviceIDInput struct {
	ID string `json:"id" jsonschema:"id do PC (campo id de devices_list)"`
}

type deviceControlInput struct {
	ID     string `json:"id" jsonschema:"id do PC"`
	Action string `json:"action" jsonschema:"message | lock | restart | shutdown"`
	Text   string `json:"text,omitempty" jsonschema:"texto do aviso (só para action=message, até 300 caracteres)"`
}

type deviceCommandRunInput struct {
	ID         string `json:"id" jsonschema:"id do PC"`
	PowerShell string `json:"powershell" jsonschema:"comando PowerShell (até 4000 caracteres)"`
}

type deviceCommandResultInput struct {
	ID        string `json:"id" jsonschema:"id do PC"`
	CommandID string `json:"commandId" jsonschema:"commandId devolvido por device_command_run"`
}

func (s *Server) addDeviceTools(srv *mcp.Server) {
	base := s.cfg.AuthBaseURL

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "devices_list",
		Description: "Lista os PCs do laboratório (nome, online/último contato, CPU/RAM/GPU, apps abertos). Exige permissão dispositivos:ver.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxyUntrusted(ctx, req, "GET", base+"/hour-lab-devices", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "device_commands",
		Description: "Histórico auditado de um PC: comandos (com resultado), travar/reiniciar/desligar/aviso e sessões de shell — quem fez, quando e por onde. Exige dispositivos:ver.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deviceIDInput) (*mcp.CallToolResult, any, error) {
		if !deviceIDRe.MatchString(in.ID) {
			return errResult("id inválido"), nil, nil
		}
		return s.proxyUntrusted(ctx, req, "GET", base+"/hour-lab-devices/"+in.ID+"/commands", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "device_control",
		Description: "Ação num PC: message (aviso na tela), lock (trava a sessão), restart, shutdown. Chega em ~1s se o PC estiver ligado. Fica auditado. Exige dispositivos:controlar.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deviceControlInput) (*mcp.CallToolResult, any, error) {
		if !deviceIDRe.MatchString(in.ID) {
			return errResult("id inválido"), nil, nil
		}
		switch in.Action {
		case "lock", "restart", "shutdown":
			return s.proxy(ctx, req, "POST", base+"/hour-lab-devices/"+in.ID+"/"+in.Action, nil)
		case "message":
			if in.Text == "" || len([]rune(in.Text)) > 300 {
				return errResult("message exige text com até 300 caracteres"), nil, nil
			}
			return s.proxy(ctx, req, "POST", base+"/hour-lab-devices/"+in.ID+"/message", map[string]string{"text": in.Text})
		default:
			return errResult("action deve ser message, lock, restart ou shutdown"), nil, nil
		}
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "device_command_run",
		Description: "Executa PowerShell num PC do laboratório COMO SYSTEM (acesso total à máquina) e devolve a saída. " +
			"Espera até 45s; se o PC não responder a tempo, devolve o commandId pra consultar com device_command_result. " +
			"Cada comando fica gravado (quem, PC, texto, resultado). Exige dispositivos:executar. Timeout no PC: 60s.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deviceCommandRunInput) (*mcp.CallToolResult, any, error) {
		if !deviceIDRe.MatchString(in.ID) {
			return errResult("id inválido"), nil, nil
		}
		if in.PowerShell == "" || len(in.PowerShell) > 4000 {
			return errResult("powershell obrigatório (até 4000 caracteres)"), nil, nil
		}
		auth := authorization(req.Extra)
		ctx, cancel := context.WithTimeout(ctx, deviceRunWait+10*time.Second)
		defer cancel()
		enqURL := base + "/hour-lab-devices/" + in.ID + "/command"
		status, raw, err := s.client.do(ctx, "POST", enqURL, auth, map[string]string{"text": in.PowerShell})
		if err != nil || status >= 400 {
			return resultFromMode("POST", enqURL, status, raw, err, false)
		}
		var enq struct {
			CommandID string `json:"commandId"`
		}
		if json.Unmarshal(raw, &enq) != nil || enq.CommandID == "" {
			return errResult("resposta inesperada ao enfileirar: " + string(raw)), nil, nil
		}
		getURL := base + "/hour-lab-devices/" + in.ID + "/commands/" + enq.CommandID
		deadline := time.Now().Add(deviceRunWait)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return textResult(fmt.Sprintf(`{"commandId":%q,"status":"aguardando"}`, enq.CommandID)), nil, nil
			case <-time.After(devicePollInterval):
			}
			st, body, err := s.client.do(ctx, "GET", getURL, auth, nil)
			if err != nil || st >= 400 {
				continue
			}
			var got struct {
				Command struct {
					Result *string `json:"result"`
				} `json:"command"`
			}
			if json.Unmarshal(body, &got) == nil && got.Command.Result != nil {
				return resultFromMode("GET", getURL, st, body, nil, true)
			}
		}
		return textResult(fmt.Sprintf(`{"commandId":%q,"status":"aguardando","dica":"PC pode estar desligado ou ocupado; use device_command_result"}`, enq.CommandID)), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "device_command_result",
		Description: "Consulta o resultado de um comando enviado por device_command_run (result null = ainda não voltou). Exige dispositivos:ver.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deviceCommandResultInput) (*mcp.CallToolResult, any, error) {
		if !deviceIDRe.MatchString(in.ID) || !deviceIDRe.MatchString(in.CommandID) {
			return errResult("id/commandId inválido"), nil, nil
		}
		return s.proxyUntrusted(ctx, req, "GET", base+"/hour-lab-devices/"+in.ID+"/commands/"+in.CommandID, nil)
	})
}
```
Confirme que `errResult` e `textResult` existem com essas assinaturas (`grep -n "func errResult\|func textResult" apps/mcp-go/*.go`) e que `s.client.do(ctx, method, url, auth string, body any) (int, []byte, error)` bate com `server.go` (usada em `proxyUntrusted`). Em `server.go`, `MCP()`: acrescentar `s.addDeviceTools(srv)` depois de `s.addAgendaTools(srv)`.

- [ ] **Step 4: Testes + gate**

Run: `cd apps/mcp-go && PATH=$PATH:$HOME/.local/bin bash -c 'gofmt -l . ; go vet ./... && go build ./... && go test ./...'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/mcp-go && git commit -m "feat(mcp-go): tools de dispositivos do laboratório (listar, controlar, executar)"
```

---

### Task 11: Deploy, permissão do Gabriel e validação ponta a ponta

**Files:** nenhum código novo (operação). Atualizar a memória `project_rbac_permissoes_individuais` no fim.

- [ ] **Step 1: Revisão antes de subir**

Rodar `/code-review` e `/security-review` no diff da branch (`git diff master...feat/acesso-remoto-frota`). Corrigir achados reais antes de seguir.

- [ ] **Step 2: Deploy da api-go e do mcp-go**

**Confirmar com o Guilherme** antes do merge (deploy de produção). Depois: `git checkout master && git merge --no-ff feat/acesso-remoto-frota && git push origin master` — o Coolify redeploya `api-go` e `mcp-go` pelos `watch_paths`. Esperar tempo fixo (~4 min) e checar:
```bash
curl -fsS https://api.santos-tech.com/ready && curl -fsS https://api.santos-tech.com/mcp/health
ssh contabo "docker exec teu3pudygggy3yk56wgqaurx psql -U postgres -d santos-tech -Atc \"SELECT to_regclass('hour_lab_device_commands'), (SELECT count(*) FROM information_schema.columns WHERE table_name='users' AND column_name='permissions')\""
```
Expected: `ready` 200; `hour_lab_device_commands|1`.

- [ ] **Step 3: Watchdog** — executar Task 9 Steps 5 e 6 (canário → frota).

- [ ] **Step 4: Permissão do Gabriel**

Perguntar ao Guilherme quais ações (sugestão: `ver`, `controlar`, `executar`; `gerenciar` fica com admin). Aplicar pelo painel/API (PATCH com sudo) ou, se ainda sem front, direto:
```bash
ssh contabo "docker exec teu3pudygggy3yk56wgqaurx psql -U postgres -d santos-tech -c \"UPDATE users SET permissions = '{\\\"dispositivos\\\":[\\\"ver\\\",\\\"controlar\\\",\\\"executar\\\"]}' WHERE id = 55 RETURNING id, permissions\""
```
e invalidar o cache do usuário: `ssh contabo "docker exec <container-redis> redis-cli DEL <cacheUserKey(55)>"` (nome da chave: ver `cacheUserKey` em `apps/api-go/cache.go`; TTL é 30s, então esperar 30s também resolve).

- [ ] **Step 5: Validação ponta a ponta**

Com um PAT do Gabriel (ou pedindo pra ele): `devices_list` → lista; `device_command_run` num PC ligado com `hostname` → saída em < 10s; `GET /auth/admin/users` com o token dele → 403 (continua sem admin); `device_commands` mostra a linha com `source` correto. Checar no banco: `SELECT kind, source, user_id, left(text,30), result IS NOT NULL FROM hour_lab_device_commands ORDER BY created_at DESC LIMIT 5`.

- [ ] **Step 6: Memória**

Atualizar `~/.claude/projects/-home-guilherme/memory/project_rbac_permissoes_individuais.md` com o que foi entregue, o que ficou pro Plano 2 e as pegadinhas que aparecerem.
