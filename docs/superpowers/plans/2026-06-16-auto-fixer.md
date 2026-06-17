# Auto-Fixer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Um serviço Go autônomo que escuta webhooks de falha de deploy da Coolify, roda o Claude Code num clone do repo pra consertar o build, faz push na branch principal, aguarda o rebuild e reporta no grupo do WhatsApp usando a própria mensagem de log como âncora (reação de status + reply com diagnóstico).

**Architecture:** Serviço `net/http` + fila `asynq` sobre um Redis dedicado (sidecar, AOF ligado), rodando no Contabo via docker-compose próprio, **fora da Coolify** (imune a cair junto com os apps que conserta). O webhook responde 200 na hora e enfileira; workers processam os jobs `incident:create → fix:run → (await via 2º webhook) → notify:final`, com `deploy:timeout` agendado cobrindo o rebuild que nunca chega. Estado do incidente vive no Redis (chaves por app e por SHA). Fala **direto com a Evolution** (reação/reply), nunca via bot-go.

**Tech Stack:** Go 1.25 · `net/http` stdlib · `hibiken/asynq` (fila/retry/scheduling sobre Redis) · `redis/go-redis/v9` · `claude` CLI (`-p --output-format stream-json`) · git CLI · Evolution API (Baileys) · Coolify API v1.

## Global Constraints

- **Go 1.25**, `package main` flat (mesmo padrão de `apps/bot-go` e `apps/api-go`).
- **`go` está em `~/.local/bin`** — todo comando go roda com prefixo: `PATH=$PATH:$HOME/.local/bin <cmd>`.
- **Sem framework HTTP** — mux stdlib com padrões `"POST /rota"`.
- **Erros e logs em português** (`slog` estruturado em stdout); mensagens de erro `{code, message}` onde houver resposta JSON.
- **Degradar com graça:** faltou credencial/ferramenta → logar e seguir, nunca travar o boot nem inventar valor (cadeia de fallback do CLAUDE.md do repo).
- **Independência total:** o fixer não pode depender de bot-go, agent-go nem do Postgres/Redis compartilhado da Coolify. Estado e fila no Redis **próprio** (sidecar).
- **Loop-guard:** no máximo `MAX_FIX_ATTEMPTS` (default 2) tentativas de fix por incidente; mensagem de deploy contendo `[skip auto-fix]` nunca dispara fix.
- **Pré-commit (obrigatório, deste repo):** `PATH=$PATH:$HOME/.local/bin gofmt -l .` (vazio) · `go vet .` · `go build .` · `go test .` — tudo passando antes de cada commit.
- **Sem segredos no commit** — tokens só via env (`.env` no `.gitignore`).

---

## File Structure

Todos em `apps/auto-fixer/` (`package main`, flat), salvo indicação:

| Arquivo | Responsabilidade |
|---------|------------------|
| `main.go` | Boot: carrega config, abre Redis, sobe asynq server (workers) + client (enqueue), monta mux HTTP, registra handlers e mux de tasks. |
| `config.go` | `Config` lido de env + `loadConfig()`. Sem default só nos campos críticos (Redis, Evolution, Coolify, grupo). |
| `coolify_parse.go` | Parsing defensivo do payload do webhook (copiado/adaptado do bot-go): `coolifyDeployStatus`, `coolifyAppName`, `coolifyString`, `shortSHA`. Puro, sem IO. |
| `coolify.go` | `CoolifyClient`: `DeploymentInfo` (commit), `BuildLogs` (logs de BUILD do deployment), `ResolveAppRepo` (app→git repo/branch). |
| `evolution.go` | `EvolutionClient`: `SendText` (com `quoted` opcional → retorna msg id), `SendReaction`. Reconstrói a `key` do grupo. |
| `webhook.go` | `handleCoolifyWebhook`: valida token, parseia, decide acionável, enfileira `incident:create`. Responde 200 sempre. |
| `incident.go` | `Incident` struct + persistência no Redis: `saveIncident`, `loadIncident`, índices `active:<app>` e `bySHA:<sha>`, contador de tentativas, dedupe de deployment_uuid. |
| `jobs.go` | Nomes dos tasks, structs de payload, `newTask*` builders (enqueue tipado). |
| `claude.go` | `runClaudeFix`: spawn `claude -p` no workdir, parseia stream-json até `result`, devolve resumo + se houve mudança. |
| `gitops.go` | `cloneRepo` (askpass com GITHUB_TOKEN), `commitAll`, `pushBranch`, `headSHA`. |
| `worker_incident.go` | Handler `incident:create`: posta log âncora (Evolution), reage 🔧, grava incidente, enfileira `fix:run`. |
| `worker_fix.go` | Handler `fix:run`: clona, puxa build logs, roda Claude, commita+push, marca `awaiting_rebuild`, agenda `deploy:timeout`. |
| `worker_notify.go` | Handler `notify:final`: troca reação 🔧→✅/💀 e dá reply no log âncora com o diagnóstico. |
| `worker_timeout.go` | Handler `deploy:timeout`: se incidente ainda `awaiting_rebuild`, reage ⏰ + reply "rebuild não confirmado". |
| `Dockerfile` | Molde do `Dockerfile.agent-go`: node:22-slim + `@anthropic-ai/claude-code` + git + ripgrep + binário Go. |
| `docker-compose.yml` | `auto-fixer` + `redis` (sidecar, `--appendonly yes`); volumes pra workspaces e dados do redis. |
| `.env.example` | Referência de todas as envs. |
| `README.md` | Como rodar local, deploy no Contabo, como apontar o webhook da Coolify. |
| `coolify_parse_test.go`, `evolution_test.go`, `incident_test.go`, `claude_test.go`, `webhook_test.go` | Testes unitários (sem rede; Evolution/Coolify via `httptest`, Redis via `miniredis`). |

---

## Task 1: Scaffold + config + parsing puro

**Files:**
- Create: `apps/auto-fixer/go.mod`, `apps/auto-fixer/config.go`, `apps/auto-fixer/coolify_parse.go`
- Test: `apps/auto-fixer/coolify_parse_test.go`

**Interfaces:**
- Produces: `Config` struct + `loadConfig() (Config, error)`; `coolifyDeployStatus(map[string]any) string` (`"failed"|"success"|"unhealthy"|"started"|""`); `coolifyAppName(map[string]any) string`; `coolifyString(map[string]any, ...string) string`; `shortSHA(string) string`.

- [ ] **Step 1: Init do módulo**

Run:
```bash
cd apps/auto-fixer && PATH=$PATH:$HOME/.local/bin go mod init auto-fixer
```

- [ ] **Step 2: Teste do parsing (falha)**

Copie a lógica-alvo do bot-go (`handlers_coolify.go:55-105,108-127`). Crie `coolify_parse_test.go`:

```go
package main

import "testing"

func TestCoolifyDeployStatus(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"falha topo", map[string]any{"status": "failed"}, "failed"},
		{"erro na msg", map[string]any{"message": "build ERROR exit 1"}, "failed"},
		{"sucesso", map[string]any{"status": "success"}, "success"},
		{"deployment aninhado", map[string]any{"deployment": map[string]any{"status": "failed"}}, "failed"},
		{"started ignora", map[string]any{"status": "queued"}, "started"},
		{"desconhecido", map[string]any{"foo": "bar"}, ""},
		{"falha tem prioridade", map[string]any{"status": "running", "message": "fail"}, "failed"},
	}
	for _, c := range cases {
		if got := coolifyDeployStatus(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestCoolifyAppName(t *testing.T) {
	if got := coolifyAppName(map[string]any{"application_name": "bot-go"}); got != "bot-go" {
		t.Errorf("got %q", got)
	}
	if got := coolifyAppName(map[string]any{"deployment": map[string]any{"name": "api-go"}}); got != "api-go" {
		t.Errorf("aninhado: got %q", got)
	}
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("abcdef1234567"); got != "abcdef1" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 3: Rodar teste (deve falhar a compilar)**

Run: `cd apps/auto-fixer && PATH=$PATH:$HOME/.local/bin go test .`
Expected: FAIL — `undefined: coolifyDeployStatus`.

- [ ] **Step 4: Implementar `coolify_parse.go`**

Copie verbatim de `apps/bot-go/handlers_coolify.go` as funções `coolifyDeployStatus`, `coolifyAppName`, `coolifyString` e `shortSHA` (linhas 55-158). São puras, sem dependência do `Server`. Coloque `package main` no topo.

- [ ] **Step 5: Rodar teste (passa)**

Run: `cd apps/auto-fixer && PATH=$PATH:$HOME/.local/bin go test .`
Expected: PASS.

- [ ] **Step 6: Implementar `config.go`**

```go
package main

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port            string // PORT (default 3340)
	WebhookSecret   string // COOLIFY_WEBHOOK_SECRET (token na query)
	RedisURL        string // REDIS_URL (sidecar)
	CoolifyAPIURL   string // COOLIFY_API_URL
	CoolifyAPIToken string // COOLIFY_API_TOKEN
	EvolutionURL    string // EVOLUTION_API_URL
	EvolutionKey    string // EVOLUTION_API_KEY
	EvolutionInst   string // EVOLUTION_INSTANCE
	GroupJID        string // NOTIF_GROUP_JID (ex: 1203...@g.us)
	ClaudeBin       string // CLAUDE_BIN (default "claude")
	ClaudeModel     string // CLAUDE_DEFAULT_MODEL (default "sonnet")
	ClaudeOAuth     string // CLAUDE_CODE_OAUTH_TOKEN
	GithubToken     string // GITHUB_TOKEN (askpass + MCP)
	WorkspaceRoot   string // WORKSPACE_ROOT (default /data/workspaces)
	MaxFixAttempts  int    // MAX_FIX_ATTEMPTS (default 2)
	RebuildTimeout  int    // REBUILD_TIMEOUT_MIN (default 15)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadConfig() (Config, error) {
	c := Config{
		Port:            env("PORT", "3340"),
		WebhookSecret:   os.Getenv("COOLIFY_WEBHOOK_SECRET"),
		RedisURL:        env("REDIS_URL", "redis://localhost:6379/0"),
		CoolifyAPIURL:   os.Getenv("COOLIFY_API_URL"),
		CoolifyAPIToken: os.Getenv("COOLIFY_API_TOKEN"),
		EvolutionURL:    os.Getenv("EVOLUTION_API_URL"),
		EvolutionKey:    os.Getenv("EVOLUTION_API_KEY"),
		EvolutionInst:   os.Getenv("EVOLUTION_INSTANCE"),
		GroupJID:        os.Getenv("NOTIF_GROUP_JID"),
		ClaudeBin:       env("CLAUDE_BIN", "claude"),
		ClaudeModel:     env("CLAUDE_DEFAULT_MODEL", "sonnet"),
		ClaudeOAuth:     os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"),
		GithubToken:     os.Getenv("GITHUB_TOKEN"),
		WorkspaceRoot:   env("WORKSPACE_ROOT", "/data/workspaces"),
		MaxFixAttempts:  atoiDef(os.Getenv("MAX_FIX_ATTEMPTS"), 2),
		RebuildTimeout:  atoiDef(os.Getenv("REBUILD_TIMEOUT_MIN"), 15),
	}
	// Críticos: sem eles o serviço não funciona — boot falha cedo e claro.
	for k, v := range map[string]string{
		"REDIS_URL": c.RedisURL, "COOLIFY_API_URL": c.CoolifyAPIURL,
		"COOLIFY_API_TOKEN": c.CoolifyAPIToken, "EVOLUTION_API_URL": c.EvolutionURL,
		"EVOLUTION_API_KEY": c.EvolutionKey, "EVOLUTION_INSTANCE": c.EvolutionInst,
		"NOTIF_GROUP_JID": c.GroupJID, "CLAUDE_CODE_OAUTH_TOKEN": c.ClaudeOAuth,
		"GITHUB_TOKEN": c.GithubToken,
	} {
		if v == "" {
			return c, fmt.Errorf("config: %s obrigatório", k)
		}
	}
	return c, nil
}

func atoiDef(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}
```

- [ ] **Step 7: Build + commit**

Run: `cd apps/auto-fixer && PATH=$PATH:$HOME/.local/bin gofmt -w . && PATH=$PATH:$HOME/.local/bin go build . && PATH=$PATH:$HOME/.local/bin go test .`
Expected: build OK, testes PASS.

```bash
git add apps/auto-fixer/
git commit -m "feat(auto-fixer): scaffold, config e parsing do webhook Coolify"
```

---

## Task 2: Cliente Evolution (reação + reply)

**Files:**
- Create: `apps/auto-fixer/evolution.go`
- Test: `apps/auto-fixer/evolution_test.go`

**Interfaces:**
- Consumes: `Config`.
- Produces: `EvolutionClient` com `NewEvolutionClient(url, key, instance string) *EvolutionClient`; `SendText(ctx, to, text string, quotedID string) (msgID string, err error)` (quotedID="" → sem reply); `SendReaction(ctx, to, msgID, emoji string) error`.

**Notas de contrato (Evolution v2 / Baileys):**
- `POST {url}/message/sendText/{instance}` — body `{"number","text","quoted":{"key":{...}}}` (quoted opcional). Resposta traz `key.id`.
- `POST {url}/message/sendReaction/{instance}` — body `{"key":{"remoteJid","fromMe","id"},"reaction":"🔧"}`.
- A `key` de uma mensagem **que nós enviamos** ao grupo se reconstrói como `{remoteJid: <grupo @g.us>, fromMe: true, id: <msgID>}`. Reaction com `reaction:""` remove.

- [ ] **Step 1: Teste com httptest (falha)**

```go
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendTextWithQuoted(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"key":{"id":"MSG123"}}`))
	}))
	defer srv.Close()

	c := NewEvolutionClient(srv.URL, "k", "inst")
	id, err := c.SendText(context.Background(), "120@g.us", "oi", "PARENT")
	if err != nil || id != "MSG123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if gotPath != "/message/sendText/inst" {
		t.Errorf("path=%q", gotPath)
	}
	q, ok := gotBody["quoted"].(map[string]any)
	if !ok {
		t.Fatalf("sem quoted: %v", gotBody)
	}
	key := q["key"].(map[string]any)
	if key["id"] != "PARENT" {
		t.Errorf("quoted.key.id=%v", key["id"])
	}
}

func TestSendReaction(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewEvolutionClient(srv.URL, "k", "inst")
	if err := c.SendReaction(context.Background(), "120@g.us", "MSG123", "🔧"); err != nil {
		t.Fatal(err)
	}
	key := gotBody["key"].(map[string]any)
	if key["remoteJid"] != "120@g.us" || key["fromMe"] != true || key["id"] != "MSG123" {
		t.Errorf("key errada: %v", key)
	}
	if gotBody["reaction"] != "🔧" {
		t.Errorf("reaction=%v", gotBody["reaction"])
	}
}
```

- [ ] **Step 2: Rodar (falha)** — `go test .` → `undefined: NewEvolutionClient`.

- [ ] **Step 3: Implementar `evolution.go`**

Base no `apps/bot-go/evolution.go` (mesma normalização de `to` p/ grupos), estendido:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type EvolutionClient struct {
	url, apiKey, instance string
	http                  *http.Client
}

func NewEvolutionClient(url, apiKey, instance string) *EvolutionClient {
	return &EvolutionClient{
		url:      strings.TrimRight(url, "/"),
		apiKey:   apiKey,
		instance: instance,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

func normalizeTo(to string) string {
	if strings.HasSuffix(to, "@g.us") {
		return to
	}
	n := strings.TrimSuffix(to, "@s.whatsapp.net")
	if i := strings.IndexAny(n, "@:"); i > 0 {
		n = n[:i]
	}
	return n
}

func (c *EvolutionClient) post(ctx context.Context, path string, body any) ([]byte, error) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/%s/%s", c.url, strings.Trim(path, "/"), c.instance),
		bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("evolution %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("evolution %s status %d: %s", path, resp.StatusCode, string(raw))
	}
	return raw, nil
}

// SendText envia texto; quotedID != "" faz a mensagem ser um reply à msg original.
// Retorna o id da nova mensagem.
func (c *EvolutionClient) SendText(ctx context.Context, to, text, quotedID string) (string, error) {
	body := map[string]any{"number": normalizeTo(to), "text": text}
	if quotedID != "" {
		body["quoted"] = map[string]any{
			"key": map[string]any{"remoteJid": to, "fromMe": true, "id": quotedID},
		}
	}
	raw, err := c.post(ctx, "message/sendText", body)
	if err != nil {
		return "", err
	}
	var out struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Key.ID, nil
}

// SendReaction reage a uma mensagem nossa no grupo (emoji "" remove a reação).
func (c *EvolutionClient) SendReaction(ctx context.Context, to, msgID, emoji string) error {
	body := map[string]any{
		"key":      map[string]any{"remoteJid": to, "fromMe": true, "id": msgID},
		"reaction": emoji,
	}
	_, err := c.post(ctx, "message/sendReaction", body)
	return err
}
```

- [ ] **Step 4: Rodar (passa)** — `go test .` → PASS.
- [ ] **Step 5: Commit**

```bash
cd apps/auto-fixer && PATH=$PATH:$HOME/.local/bin gofmt -w .
git add apps/auto-fixer/evolution.go apps/auto-fixer/evolution_test.go
git commit -m "feat(auto-fixer): cliente Evolution com reação e reply"
```

> **Validação manual pendente (Task 9):** confirmar que a Evolution v2.3.7 aceita `sendReaction` e `quoted` (o endpoint pode variar). Há fallback: se `sendReaction` falhar, o worker degrada pra só reply de texto.

---

## Task 3: Estado de incidente no Redis

**Files:**
- Create: `apps/auto-fixer/incident.go`
- Test: `apps/auto-fixer/incident_test.go`

**Interfaces:**
- Consumes: `*redis.Client`.
- Produces:
  - `type Incident struct { ID, App, Repo, Branch, Commit, FixSHA, AnchorMsgID, Status string; Attempts int; DeploymentUUID string }`
  - `Status` ∈ `"fixing" | "awaiting_rebuild" | "done" | "failed"`.
  - `type Store struct { rdb *redis.Client }` com:
    - `(s *Store) Save(ctx, *Incident) error`
    - `(s *Store) Load(ctx, id string) (*Incident, error)`
    - `(s *Store) ActiveByApp(ctx, app string) (*Incident, error)`
    - `(s *Store) SetActive(ctx, *Incident) error` / `ClearActive(ctx, app string) error`
    - `(s *Store) BindSHA(ctx, sha, id string) error` / `BySHA(ctx, sha string) (*Incident, error)`
    - `(s *Store) DedupeDeployment(ctx, uuid string, ttl time.Duration) (bool, error)` (SET NX; true=primeira vez)

- [ ] **Step 1: Adicionar deps**

Run:
```bash
cd apps/auto-fixer && PATH=$PATH:$HOME/.local/bin go get github.com/redis/go-redis/v9 github.com/alicebob/miniredis/v2 github.com/hibiken/asynq github.com/google/uuid
```

- [ ] **Step 2: Teste com miniredis (falha)**

```go
package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return &Store{rdb: redis.NewClient(&redis.Options{Addr: mr.Addr()})}
}

func TestIncidentRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := &Incident{ID: "i1", App: "bot-go", Status: "fixing", Attempts: 1}
	if err := s.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, "i1")
	if err != nil || got.App != "bot-go" || got.Attempts != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestActiveByApp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := &Incident{ID: "i1", App: "bot-go", Status: "awaiting_rebuild"}
	_ = s.Save(ctx, in)
	if err := s.SetActive(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ActiveByApp(ctx, "bot-go")
	if got == nil || got.ID != "i1" {
		t.Fatalf("active=%+v", got)
	}
	_ = s.ClearActive(ctx, "bot-go")
	got, _ = s.ActiveByApp(ctx, "bot-go")
	if got != nil {
		t.Fatalf("deveria limpar: %+v", got)
	}
}

func TestBySHAandDedupe(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := &Incident{ID: "i1", App: "bot-go"}
	_ = s.Save(ctx, in)
	_ = s.BindSHA(ctx, "deadbeef", "i1")
	got, _ := s.BySHA(ctx, "deadbeef")
	if got == nil || got.ID != "i1" {
		t.Fatalf("bySHA=%+v", got)
	}
	first, _ := s.DedupeDeployment(ctx, "uuid-1", time.Minute)
	second, _ := s.DedupeDeployment(ctx, "uuid-1", time.Minute)
	if !first || second {
		t.Fatalf("dedupe: first=%v second=%v", first, second)
	}
}
```

- [ ] **Step 3: Rodar (falha)** — `undefined: Store`.

- [ ] **Step 4: Implementar `incident.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

type Incident struct {
	ID             string `json:"id"`
	App            string `json:"app"`
	Repo           string `json:"repo"`
	Branch         string `json:"branch"`
	Commit         string `json:"commit"`
	FixSHA         string `json:"fix_sha"`
	AnchorMsgID    string `json:"anchor_msg_id"`
	Status         string `json:"status"`
	Attempts       int    `json:"attempts"`
	DeploymentUUID string `json:"deployment_uuid"`
}

type Store struct{ rdb *redis.Client }

func (s *Store) Save(ctx context.Context, in *Incident) error {
	b, _ := json.Marshal(in)
	return s.rdb.Set(ctx, "incident:"+in.ID, b, 7*24*time.Hour).Err()
}

func (s *Store) Load(ctx context.Context, id string) (*Incident, error) {
	b, err := s.rdb.Get(ctx, "incident:"+id).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var in Incident
	return &in, json.Unmarshal(b, &in)
}

func (s *Store) SetActive(ctx context.Context, in *Incident) error {
	return s.rdb.Set(ctx, "active:"+in.App, in.ID, 24*time.Hour).Err()
}

func (s *Store) ClearActive(ctx context.Context, app string) error {
	return s.rdb.Del(ctx, "active:"+app).Err()
}

func (s *Store) ActiveByApp(ctx context.Context, app string) (*Incident, error) {
	id, err := s.rdb.Get(ctx, "active:"+app).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Load(ctx, id)
}

func (s *Store) BindSHA(ctx context.Context, sha, id string) error {
	return s.rdb.Set(ctx, "sha:"+sha, id, 24*time.Hour).Err()
}

func (s *Store) BySHA(ctx context.Context, sha string) (*Incident, error) {
	id, err := s.rdb.Get(ctx, "sha:"+sha).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Load(ctx, id)
}

// DedupeDeployment retorna true se este deployment_uuid não foi visto na janela.
func (s *Store) DedupeDeployment(ctx context.Context, uuid string, ttl time.Duration) (bool, error) {
	return s.rdb.SetNX(ctx, "seen:"+uuid, "1", ttl).Result()
}
```

- [ ] **Step 5: Rodar (passa)** — `go test .` → PASS.
- [ ] **Step 6: Commit**

```bash
cd apps/auto-fixer && PATH=$PATH:$HOME/.local/bin gofmt -w .
git add apps/auto-fixer/incident.go apps/auto-fixer/incident_test.go apps/auto-fixer/go.mod apps/auto-fixer/go.sum
git commit -m "feat(auto-fixer): estado de incidente no Redis (ativo/sha/dedupe)"
```

---

## Task 4: Cliente Coolify (commit + logs de BUILD)

**Files:**
- Create: `apps/auto-fixer/coolify.go`
- Test: `apps/auto-fixer/coolify_test.go`

**Interfaces:**
- Produces: `CoolifyClient` com `NewCoolifyClient(url, token string)`; `DeploymentInfo(ctx, uuid) (commit, message string, err error)`; `BuildLogs(ctx, deploymentUUID string) (string, error)`; `AppRepo(ctx, appUUID string) (repo, branch string, err error)`.

**Notas de contrato (Coolify API v1):**
- `GET /api/v1/deployments/{uuid}` → deployment com `commit`, `commit_message` e `logs` (string JSON com linhas de build no campo `logs`; cada linha tem `output`). Pegamos o campo `logs` e extraímos os `output` em texto plano. **Este é o log de BUILD** (não o runtime do `applications/{uuid}/logs` que o bot-go usa).
- `GET /api/v1/applications/{uuid}` → app com `git_repository` e `git_branch`.

- [ ] **Step 1: Teste httptest (falha)** — cobre `DeploymentInfo` e `BuildLogs` parseando `{"commit":"abc","commit_message":"x","logs":"[{\"output\":\"line1\"},{\"output\":\"line2\"}]"}` → BuildLogs deve devolver `"line1\nline2"`. E `AppRepo` parseando `{"git_repository":"https://github.com/org/r.git","git_branch":"main"}`.

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildLogsExtractsOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"commit":"abc1234","commit_message":"quebrou","logs":"[{\"output\":\"npm ERR\"},{\"output\":\"exit 1\"}]"}`))
	}))
	defer srv.Close()
	c := NewCoolifyClient(srv.URL, "t")
	commit, msg, err := c.DeploymentInfo(context.Background(), "d1")
	if err != nil || commit != "abc1234" || msg != "quebrou" {
		t.Fatalf("commit=%q msg=%q err=%v", commit, msg, err)
	}
	logs, err := c.BuildLogs(context.Background(), "d1")
	if err != nil || logs != "npm ERR\nexit 1" {
		t.Fatalf("logs=%q err=%v", logs, err)
	}
}

func TestAppRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"git_repository":"https://github.com/org/r.git","git_branch":"main"}`))
	}))
	defer srv.Close()
	c := NewCoolifyClient(srv.URL, "t")
	repo, branch, err := c.AppRepo(context.Background(), "a1")
	if err != nil || repo != "https://github.com/org/r.git" || branch != "main" {
		t.Fatalf("repo=%q branch=%q err=%v", repo, branch, err)
	}
}
```

- [ ] **Step 2: Rodar (falha)**.

- [ ] **Step 3: Implementar `coolify.go`** — reusa o `do()` de `apps/bot-go/coolify.go:43-64` (Bearer + LimitReader). `DeploymentInfo` igual ao do bot-go. Acrescenta:

```go
// BuildLogs extrai o log de BUILD do deployment. O campo "logs" vem como string
// JSON de objetos {output:...}; concatena os outputs em texto plano.
func (c *CoolifyClient) BuildLogs(ctx context.Context, deploymentUUID string) (string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/deployments/"+deploymentUUID)
	if err != nil {
		return "", err
	}
	var d struct {
		Logs string `json:"logs"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", err
	}
	if d.Logs == "" {
		return "", nil
	}
	var lines []struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(d.Logs), &lines); err != nil {
		// Formato inesperado: devolve cru (melhor que nada pro Claude ler).
		return d.Logs, nil
	}
	var out []string
	for _, l := range lines {
		out = append(out, l.Output)
	}
	return strings.Join(out, "\n"), nil
}

// AppRepo retorna o repositório git e a branch configurados na aplicação.
func (c *CoolifyClient) AppRepo(ctx context.Context, appUUID string) (repo, branch string, err error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/applications/"+appUUID)
	if err != nil {
		return "", "", err
	}
	var a struct {
		GitRepository string `json:"git_repository"`
		GitBranch     string `json:"git_branch"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", "", err
	}
	return a.GitRepository, a.GitBranch, nil
}
```

- [ ] **Step 4: Rodar (passa)**. **Step 5: Commit** `feat(auto-fixer): cliente Coolify com logs de build e repo do app`.

> **Validação manual pendente (Task 9):** o shape exato do campo `logs` e de `git_repository` na sua Coolify precisa ser confirmado com um `curl` real (ver README). O parser cai pra "cru" se o formato divergir.

---

## Task 5: git + spawn do Claude

**Files:**
- Create: `apps/auto-fixer/gitops.go`, `apps/auto-fixer/claude.go`
- Test: `apps/auto-fixer/claude_test.go`

**Interfaces:**
- Produces:
  - `cloneRepo(ctx, repoURL, branch, dest, githubToken string) error`
  - `commitAll(ctx, dir, msg string) (changed bool, err error)`
  - `pushBranch(ctx, dir, branch string) error`
  - `headSHA(ctx, dir string) (string, error)`
  - `runClaudeFix(ctx context.Context, cfg Config, workdir, buildLogs, commitMsg string) (summary string, err error)` — spawn `claude -p`, lê stream-json, devolve o texto do evento `result`.
  - `parseClaudeResult(lines []string) (summary string)` — função pura testável que extrai o `result` do stream-json.

- [ ] **Step 1: Teste puro do parser de stream-json (falha)**

```go
package main

import "testing"

func TestParseClaudeResult(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"olhando"}]}}`,
		`{"type":"result","subtype":"success","result":"Corrigi o Dockerfile: faltava COPY do go.sum."}`,
	}
	if got := parseClaudeResult(lines); got != "Corrigi o Dockerfile: faltava COPY do go.sum." {
		t.Fatalf("got %q", got)
	}
}

func TestParseClaudeResultEmpty(t *testing.T) {
	if got := parseClaudeResult([]string{`{"type":"system"}`}); got != "" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Rodar (falha)**.

- [ ] **Step 3: Implementar `claude.go`** (spawn modelado em `apps/agent-go/session.go:292-334`):

```go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// parseClaudeResult extrai o texto do evento final "result" do stream-json.
func parseClaudeResult(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		var ev map[string]any
		if json.Unmarshal([]byte(lines[i]), &ev) != nil {
			continue
		}
		if ev["type"] == "result" {
			if s, ok := ev["result"].(string); ok {
				return s
			}
		}
	}
	return ""
}

const fixPromptTmpl = `O build/deploy deste repositório FALHOU. Sua tarefa: descobrir a causa, corrigir o código e deixar o build passando. NÃO faça mudanças não relacionadas.

Commit que quebrou: %s

--- LOGS DO BUILD (fim do log é o mais relevante) ---
%s
--- FIM DOS LOGS ---

Conserte a causa raiz. Quando terminar, descreva em 1-2 frases o que era o problema e o que você mudou. Não faça commit nem push — a orquestração cuida disso.`

func runClaudeFix(ctx context.Context, cfg Config, workdir, buildLogs, commitMsg string) (string, error) {
	prompt := fmt.Sprintf(fixPromptTmpl, commitMsg, tailLines(buildLogs, 200))
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--model", cfg.ClaudeModel,
		"--dangerously-skip-permissions", "--add-dir", workdir,
	}
	cmd := exec.CommandContext(ctx, cfg.ClaudeBin, args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"CLAUDE_CODE_OAUTH_TOKEN="+cfg.ClaudeOAuth,
		"GITHUB_TOKEN="+cfg.GithubToken,
		"GITHUB_PERSONAL_ACCESS_TOKEN="+cfg.GithubToken,
	)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var lines []string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	if err := cmd.Wait(); err != nil {
		return parseClaudeResult(lines), fmt.Errorf("claude saiu com erro: %w", err)
	}
	return parseClaudeResult(lines), nil
}
```

(`tailLines` — copie de `apps/bot-go/coolify.go:158-164` para um util compartilhado; se já estiver em `coolify.go` deste módulo, reuse.)

- [ ] **Step 4: Implementar `gitops.go`** — clone com askpass temporário (modelado em `apps/agent-go/mcp.go`: escreve script askpass em arquivo `chmod 700`, passa `GIT_ASKPASS` por env, nunca o token no argv):

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitEnvAskpass(token string) ([]string, func(), error) {
	f, err := os.CreateTemp("", "askpass-*.sh")
	if err != nil {
		return nil, func() {}, err
	}
	script := "#!/bin/sh\necho \"" + token + "\"\n"
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, func() {}, err
	}
	f.Close()
	os.Chmod(f.Name(), 0o700)
	cleanup := func() { os.Remove(f.Name()) }
	env := append(os.Environ(),
		"GIT_ASKPASS="+f.Name(),
		"GIT_TERMINAL_PROMPT=0",
	)
	return env, cleanup, nil
}

func cloneRepo(ctx context.Context, repoURL, branch, dest, token string) error {
	env, cleanup, err := gitEnvAskpass(token)
	if err != nil {
		return err
	}
	defer cleanup()
	// Insere o usuário no URL pra o askpass fornecer a senha (token).
	url := repoURL
	if strings.HasPrefix(url, "https://") && !strings.Contains(url, "@") {
		url = "https://git@" + strings.TrimPrefix(url, "https://")
	}
	args := []string{"clone", "--depth", "50", "--branch", branch, url, dest}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, out)
	}
	return nil
}

func commitAll(ctx context.Context, dir, msg string) (bool, error) {
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if _, err := run("add", "-A"); err != nil {
		return false, err
	}
	// status --porcelain vazio => nada mudou.
	st, err := run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(st) == "" {
		return false, nil
	}
	_, _ = run("config", "user.email", "auto-fixer@santos-tech.com")
	_, _ = run("config", "user.name", "auto-fixer")
	if out, err := run("commit", "-m", msg); err != nil {
		return false, fmt.Errorf("git commit: %w: %s", err, out)
	}
	return true, nil
}

func pushBranch(ctx context.Context, dir, branch, token string) error {
	env, cleanup, err := gitEnvAskpass(token)
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, "git", "push", "origin", "HEAD:"+branch)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w: %s", err, out)
	}
	return nil
}

func headSHA(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func workdirFor(root, app, id string) string { return filepath.Join(root, app+"-"+id) }
```

(Ajuste a assinatura de `pushBranch` no bloco Interfaces — leva `token`.)

- [ ] **Step 5: Rodar testes (passa)** — só o parser puro tem teste automatizado; spawn/git são validados na Task 9 (e2e). `go test .` → PASS. **Step 6: Commit** `feat(auto-fixer): spawn do Claude e operações git (clone/commit/push)`.

---

## Task 6: Jobs asynq + webhook handler

**Files:**
- Create: `apps/auto-fixer/jobs.go`, `apps/auto-fixer/webhook.go`
- Test: `apps/auto-fixer/webhook_test.go`

**Interfaces:**
- Consumes: `Config`, `*asynq.Client`, parsing da Task 1.
- Produces:
  - Constantes de task: `TaskIncidentCreate="incident:create"`, `TaskFixRun="fix:run"`, `TaskNotifyFinal="notify:final"`, `TaskDeployTimeout="deploy:timeout"`.
  - Payloads: `IncidentPayload{App, DeploymentUUID, Branch, Commit, RawStatus string}`; `IDPayload{IncidentID string}`; `NotifyPayload{IncidentID, Outcome string}` (`Outcome` ∈ `"success"|"failed"`).
  - `newIncidentTask(IncidentPayload) *asynq.Task` etc.
  - `type WebhookHandler struct { cfg Config; client *asynq.Client; store *Store; cool *CoolifyClient }` com `ServeHTTP`.

- [ ] **Step 1: Teste do handler (falha)** — `httptest` POST com token errado → 401; token certo + payload `failed` → 200 e um task `incident:create` enfileirado (use um `asynq.Client` apontando pra `miniredis`, depois inspecione via `asynq.Inspector`). Payload `success` sem incidente ativo → 200 e **nenhum** task novo. Mensagem com `[skip auto-fix]` → 200 sem task.

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
)

func TestWebhookEnqueuesOnFailure(t *testing.T) {
	mr, _ := miniredis.Run()
	t.Cleanup(mr.Close)
	opt := asynq.RedisClientOpt{Addr: mr.Addr()}
	client := asynq.NewClient(opt)
	insp := asynq.NewInspector(opt)
	h := &WebhookHandler{
		cfg:    Config{WebhookSecret: "s", GroupJID: "120@g.us"},
		client: client,
		store:  &Store{rdb: redisFromMiniredis(mr)}, // helper local
	}

	// token errado
	r := httptest.NewRequest("POST", "/webhooks/coolify?token=x", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("esperava 401, veio %d", w.Code)
	}

	// falha → enfileira
	r = httptest.NewRequest("POST", "/webhooks/coolify?token=s",
		strings.NewReader(`{"status":"failed","application_name":"bot-go","deployment_uuid":"d1"}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", w.Code)
	}
	q, _ := insp.ListPendingTasks("default")
	if len(q) != 1 {
		t.Fatalf("esperava 1 task, veio %d", len(q))
	}
}
```

(`redisFromMiniredis` e `[skip auto-fix]`/`success` casos: complete análogos. Para o caso de falha o handler busca commit via Coolify só se `cool != nil`; no teste deixe `cool=nil` para pular a chamada de rede.)

- [ ] **Step 2: Rodar (falha)**.

- [ ] **Step 3: Implementar `jobs.go`** — constantes, structs e builders:

```go
package main

import (
	"encoding/json"

	"github.com/hibiken/asynq"
)

const (
	TaskIncidentCreate = "incident:create"
	TaskFixRun         = "fix:run"
	TaskNotifyFinal    = "notify:final"
	TaskDeployTimeout  = "deploy:timeout"
)

type IncidentPayload struct {
	App, DeploymentUUID, Branch, Commit, CommitMsg string
}
type IDPayload struct{ IncidentID string }
type NotifyPayload struct {
	IncidentID, Outcome string
}

func newTask(name string, v any) *asynq.Task {
	b, _ := json.Marshal(v)
	return asynq.NewTask(name, b)
}
```

- [ ] **Step 4: Implementar `webhook.go`** — valida token (constant-time como `handlers_coolify.go:20`), parseia, gate por `coolifyDeployStatus`:
  - `"failed"`: dedupe `DeploymentUUID`; se mensagem contém `[skip auto-fix]` → 200 sem enfileirar; senão enriquece commit via `cool.DeploymentInfo` (se `cool != nil`) e enfileira `incident:create`.
  - `"success"`: se há incidente ativo `awaiting_rebuild` cujo `FixSHA` == commit do evento → enfileira `notify:final{Outcome:"success"}`. Senão ignora (deploy normal).
  - resto: 200 sem ação.
  - **Sempre responde 200** (Coolify não deve reenfileirar por erro nosso).

- [ ] **Step 5: Rodar (passa)**. **Step 6: Commit** `feat(auto-fixer): jobs asynq e handler de webhook`.

---

## Task 7: Workers (incident/fix/notify/timeout)

**Files:**
- Create: `apps/auto-fixer/worker_incident.go`, `worker_fix.go`, `worker_notify.go`, `worker_timeout.go`

**Interfaces:**
- Consumes: tudo das Tasks 2-6.
- Produces: `type Workers struct { cfg Config; store *Store; evo *EvolutionClient; cool *CoolifyClient; client *asynq.Client }` com métodos `HandleIncidentCreate`, `HandleFixRun`, `HandleNotifyFinal`, `HandleDeployTimeout` (assinatura `func(ctx, *asynq.Task) error`).

**Lógica por worker (sem teste automatizado — validado e2e na Task 9; cada um loga e degrada):**

- [ ] **Step 1: `worker_incident.go` — `HandleIncidentCreate`**
  1. Decodifica `IncidentPayload`.
  2. Resolve repo/branch: usa `payload.Branch` ou `cool.AppRepo`.
  3. Posta log âncora no grupo: `evo.SendText(ctx, cfg.GroupJID, "🔧 Build de <app> falhou (commit <sha>). Vou tentar consertar…", "")` → guarda `AnchorMsgID`.
  4. Reage 🔧: `evo.SendReaction(ctx, cfg.GroupJID, AnchorMsgID, "🔧")` (degrada se falhar).
  5. Cria `Incident{ID: uuid, App, Repo, Branch, Commit, AnchorMsgID, Status:"fixing", Attempts:1, DeploymentUUID}`, `Save` + `SetActive`.
  6. Enfileira `fix:run{IncidentID}`.

- [ ] **Step 2: `worker_fix.go` — `HandleFixRun`**
  1. `Load` incidente; se nil ou Status já `done`, retorna.
  2. `workdir := workdirFor(cfg.WorkspaceRoot, app, id)`; `cloneRepo`.
  3. `buildLogs, _ := cool.BuildLogs(ctx, incident.DeploymentUUID)` (segue mesmo vazio).
  4. `summary, err := runClaudeFix(...)`. Se err e summary vazio → marca falha e enfileira `notify:final{failed}`; `ClearActive`; return.
  5. `changed, _ := commitAll(ctx, workdir, "fix(auto): "+summary+" [skip auto-fix]")`. Se `!changed` → Claude não mudou nada → `notify:final{failed}`; `ClearActive`; return. (`[skip auto-fix]` na mensagem do commit evita que o rebuild deste push, se falhar, re-dispare o fixer — o loop-guard por Attempts é a segunda trava.)
  6. `pushBranch(ctx, workdir, incident.Branch, cfg.GithubToken)`.
  7. `sha, _ := headSHA(...)`; `incident.FixSHA=sha; incident.Status="awaiting_rebuild"`; `Save`; `BindSHA(sha, id)`.
  8. Guarda `summary` no incidente (campo extra `LastSummary` — adicione ao struct na Task 3 se preferir; ou re-gere no notify). Agenda timeout: `client.Enqueue(newTask(TaskDeployTimeout, IDPayload{id}), asynq.ProcessIn(time.Duration(cfg.RebuildTimeout)*time.Minute))`.

  > **Loop-guard:** se um webhook `failed` chegar para um app que **já** tem incidente ativo `awaiting_rebuild` com `FixSHA == commit do evento`, isso é o fix falhando. O handler de webhook (Task 6) deve, nesse caso: se `Attempts < cfg.MaxFixAttempts`, enfileirar novo `fix:run` (Attempts++); senão `notify:final{failed}` com motivo "máx tentativas". **Adicione esse ramo ao `webhook.go` da Task 6** (incluído aqui porque depende do conceito de incidente ativo).

- [ ] **Step 3: `worker_notify.go` — `HandleNotifyFinal`**
  1. `Load` incidente.
  2. `Outcome=="success"`: `SendReaction(...,"✅")` e `SendText(grupo, "✅ Corrigido em <FixSHA>: <summary>", AnchorMsgID)` (reply). `Status="done"`.
  3. `Outcome=="failed"`: `SendReaction(...,"💀")` e reply `"💀 Não consegui corrigir automaticamente (<motivo>). Precisa de um humano."`. `Status="failed"`.
  4. `ClearActive(app)`.

- [ ] **Step 4: `worker_timeout.go` — `HandleDeployTimeout`**
  1. `Load`; se `Status != "awaiting_rebuild"` → return (já resolveu).
  2. `SendReaction(...,"⏰")` + reply `"⏰ Empurrei o fix <FixSHA> mas não recebi confirmação do rebuild em <N>min. Confere manualmente."`. `Status="failed"`; `ClearActive`.

- [ ] **Step 5: Build + commit** `feat(auto-fixer): workers de incidente, fix, notificação e timeout`.

---

## Task 8: main.go + Docker + docs

**Files:**
- Create: `apps/auto-fixer/main.go`, `Dockerfile`, `docker-compose.yml`, `.env.example`, `README.md`

**Interfaces:**
- Consumes: tudo.

- [ ] **Step 1: `main.go`** — boot:
  1. `cfg, err := loadConfig()`; fatal se err.
  2. Abre Redis (`redis.ParseURL(cfg.RedisURL)`), `Store`, `EvolutionClient`, `CoolifyClient`, `asynq.Client`.
  3. `asynq.NewServer(redisOpt, asynq.Config{Concurrency: 5})` + `mux.HandleFunc(TaskIncidentCreate, w.HandleIncidentCreate)` etc. Roda em goroutine.
  4. `http.NewServeMux`: `POST /webhooks/coolify` → `WebhookHandler`; `GET /health` → 200. `http.ListenAndServe(":"+cfg.Port, ...)`.
  5. `slog` JSON em stdout; shutdown gracioso (SIGINT/TERM → `srv.Shutdown` + `asynqSrv.Shutdown`).

- [ ] **Step 2: `Dockerfile`** — molde `infra/Dockerfile.agent-go`: stage `golang:1.25-alpine` builda o binário; stage `node:22-bookworm-slim` instala `@anthropic-ai/claude-code` global + `git ca-certificates ripgrep`; copia o binário; usuário non-root; `EXPOSE 3340`; volume `/data/workspaces`.

- [ ] **Step 3: `docker-compose.yml`** — dois serviços: `auto-fixer` (build do Dockerfile, env_file `.env`, `depends_on: redis`, volume `workspaces`) e `redis` (`redis:7-alpine`, `command: redis-server --appendonly yes`, volume `redisdata`). **Não** referencia a rede da Coolify.

- [ ] **Step 4: `.env.example`** — todas as envs do `config.go` com comentário e valores de exemplo (sem segredos reais).

- [ ] **Step 5: `README.md`** — seções: o que é, como rodar local (`docker compose up`), como descobrir o shape real da Coolify (`curl -H "Authorization: Bearer $TOKEN" $URL/api/v1/deployments/<uuid>` pra confirmar campo `logs`), e **como apontar o webhook**: na Coolify, em cada app (ou global) → Notifications/Webhooks → URL `https://<host-do-fixer>/webhooks/coolify?token=<COOLIFY_WEBHOOK_SECRET>`, evento de deploy.

- [ ] **Step 6: Build + commit** `feat(auto-fixer): boot, Dockerfile, compose e docs`.

---

## Task 9: Validação e2e + apontar webhook

**Files:** nenhum de código novo; ajustes pontuais conforme os contratos reais.

- [ ] **Step 1:** Subir o stack local com `.env` real apontando pra Coolify de staging/produção e um grupo de teste. `docker compose up --build`.
- [ ] **Step 2:** Confirmar com `curl` o shape de `/api/v1/deployments/<uuid>` (campo `logs`) e `/api/v1/applications/<uuid>` (`git_repository`/`git_branch`). Ajustar `coolify.go` se divergir.
- [ ] **Step 3:** Confirmar que a Evolution v2.3.7 aceita `sendReaction` e `quoted`. Se `sendReaction` 404 → o worker já degrada pra só reply; documentar a limitação.
- [ ] **Step 4:** Disparar uma falha de build proposital num app de teste → observar: log âncora postado, reação 🔧, Claude rodando, push, rebuild, reação ✅ + reply. Validar o loop-guard forçando 2 falhas seguidas.
- [ ] **Step 5:** Apontar o webhook real da Coolify pro fixer no Contabo. Commit final de ajustes: `fix(auto-fixer): ajustes pós-validação e2e`.

---

## Segurança & Hardening (decisão: push direto + hardening)

O `auto-fixer` roda `claude --dangerously-skip-permissions` **sem humano no loop** e injeta **logs de build (dado não confiável)** no prompt → risco de prompt-injection escalando para execução arbitrária com os tokens do ambiente. Decisão do usuário (2026-06-16): manter o push direto na main, mitigando com isolamento em camadas (não ir para PR-com-aprovação).

**Já aplicado (Task 5):** validação de argv no git (`--` + regex `validRef`, só `https://`) e askpass com `printf`/aspas simples escapadas (sem command-injection).

### Task 10: Token efêmero escopado ao repo (GitHub App)
**Depende de credencial externa:** o usuário precisa criar um GitHub App na org Santos-Techrp com permissão `contents:write` e instalá-lo nos repos. Envs novas: `GH_APP_ID`, `GH_APP_PRIVATE_KEY`, `GH_APP_INSTALLATION_ID`.
- Substituir o `GITHUB_TOKEN` amplo por um **installation token** gerado por incidente (`POST /app/installations/{id}/access_tokens`, opcionalmente com `repositories:[<repo>]`), TTL ~1h.
- `runClaudeFix` e `cloneRepo`/`pushBranch` recebem esse token efêmero em vez do PAT do env. Se o Claude for sequestrado, o acesso se limita ao repo do incidente e expira sozinho.
- Fallback: sem GitHub App configurado, cai pro `GITHUB_TOKEN` do env (degrada com graça) e **loga o downgrade de postura**.

### Task 11: Execução do Claude em container efêmero
- `runClaudeFix` passa a orquestrar `docker run --rm` (imagem com `claude` + git) por incidente, em vez de `exec` no processo do fixer: rede restrita (`--network` só com o necessário p/ GitHub+API do Claude), sem montar segredos persistentes (token efêmero passado por env do container descartável), workspace montado como volume temporário.
- O fixer precisa de acesso ao Docker socket (`/var/run/docker.sock`) — documentar no compose e no README a implicação (o socket é poder de root; manter o host do fixer dedicado).
- Fallback: sem Docker disponível, cai pro `exec` direto e loga o downgrade.

## Self-Review

**Cobertura do spec (fluxo pedido pelo usuário):**
- "roda na VPS estilo Happy Coder" → Task 8 (serviço + compose no Contabo, fora da Coolify). ✓
- "recebe webhook da Coolify" → Task 6 (`webhook.go`). ✓
- "inicia fix" → Task 7 `HandleFixRun` + Task 5 (`runClaudeFix`). ✓
- "push main/master" → Task 5 (`pushBranch` na branch do app). ✓
- "await do build" → 2º webhook (Task 6, ramo success) + `deploy:timeout` (Task 7). Sem polling. ✓
- "notificação no WhatsApp" → Task 7 `HandleNotifyFinal` (reação + reply). ✓
- "workers e jobs" / "se o build que falhar for da api que manda log" → fila asynq durável + Redis sidecar (independência total). ✓
- Reação no log + reply → Task 2 (Evolution) + Task 7. ✓
- Loop-guard → Task 6/7 (`Attempts`/`MaxFixAttempts` + `[skip auto-fix]`). ✓

**Pontos que dependem de contrato externo (marcados como validação na Task 9, com fallback):** shape do log de build da Coolify; suporte a `sendReaction`/`quoted` na Evolution v2.3.7; formato exato dos eventos stream-json do `claude` v2.x.

**Decisões de design travadas com o usuário:** fixer autônomo (spawna Claude); roda no Contabo fora da Coolify; fala direto com a Evolution; fila durável (asynq/Redis).
</content>
</invoke>
