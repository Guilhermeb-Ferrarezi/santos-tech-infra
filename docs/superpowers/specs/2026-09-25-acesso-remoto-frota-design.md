# Acesso remoto da frota: permissões individuais, MCP e long-poll

Data: 2026-09-25 · Status: aguardando revisão

## Objetivo

Dar acesso remoto aos PCs do laboratório para quem não é admin (primeiro caso: Gabriel, user 55,
cargo Personalizado), por painel, MCP e SSH, sem torná-lo admin de todo o ecossistema. De quebra,
fazer comandos remotos chegarem em ~1s em vez de até ~60s.

Sucesso =
- Gabriel lista os PCs, trava/reinicia, executa PowerShell e entra por SSH, com cada comando
  auditado, e continua sem acesso a usuários, pagamentos, logs etc.
- Um `device_command_run` pelo MCP devolve o resultado em poucos segundos num PC ligado.
- Tirar a permissão de alguém remove a chave SSH dele da frota em até ~1 min.

## Decisões já tomadas (Guilherme, 25/09/2026)

1. Permissões **individuais por usuário + cargo opcional**. Efetiva = cargo ∪ individuais.
   Admin (role 3) continua passando em tudo. Cargos NÃO acabam.
2. Dispositivos: permissões **por ação, valendo para a frota toda** (sem escopo por sala/PC).
3. MCP: tools que executam comando nos PCs **liberadas** para quem tem `dispositivos:executar`,
   com auditoria de tudo.
4. Canal de comando via **long-poll** no watchdog.

Fora do escopo: escopo por sala/PC, eliminar cargos, trocar o `adminGuard` das demais áreas.

## 1. Modelo de permissões

**Dados.** `users.permissions JSONB NOT NULL DEFAULT '{}'`, mesmo formato de
`custom_roles.permissions` (`{"recurso": ["acao", ...]}`). Migração embutida em `migrate()`
(`apps/api-go/db.go`), igual às demais colunas adicionadas por lá.

**Regra efetiva** (nova função única `effectivePerms(u)`; `permGuard` e `buildProfile` usam ela):
- `role == 3` → libera tudo (inalterado).
- `allowTeacher && role == 2` → libera (inalterado).
- Senão: libera se `resource:action` ∈ `u.Permissions` ∪ permissões do cargo
  (cargo só se `role == 4 && custom_role_id != nil`).
- Vale para **qualquer** role: um Professor ou Aluno pode receber `dispositivos:ver`.

**Cargo opcional.** `PATCH /auth/admin/users/{id}` deixa de exigir `customRoleId` quando `role=4`
(`handlers_admin_users.go:207`). Usuário role 4 sem cargo = só as individuais.

**Edição.** `PATCH /auth/admin/users/{id}` aceita `permissions`. Mudar permissões de alguém exige
`adminGuard` + `sudoGuard` (conceder `dispositivos:executar` = SYSTEM na frota). Validação: chaves
e ações `^[a-z_]{1,40}$`, máx. 60 recursos × 10 ações. Após gravar, `invalidateUserCache`.

**Saída.** `/auth/me` (`buildProfile`, `db.go:1458`) passa a devolver:
- `permissions`: as **efetivas** (para qualquer role ≠ 3; admin continua `null`).
- `userPermissions`: só as individuais (para a tela de edição).

**Consumidores que mudam:** `dashboard/api` (`api/internal/httpapi/role.go`) hoje só usa
`permissions` para role 4 — passa a usar para qualquer role. `mcp-go`, `agent-go`, `bot-go`,
`cron-go`, `secrets-go` não mudam (seguem checando `role == 3` onde já checam).

## 2. Permissões de dispositivos

Recurso `dispositivos`, 5 ações. As 22 rotas de `registerLabDeviceRoutes` (`routes.go:454-511`)
trocam `adminGuard` por `permGuard("dispositivos", <ação>, false, ...)`:

| Ação | Rotas |
|---|---|
| `ver` | `GET /hour-lab-devices`, `GET /{id}/programs`, `GET /{id}/screenshots`, `GET /program-icons/{hash}`, `GET /{id}/commands` (novo) |
| `controlar` | `POST /{id}/message`, `/lock`, `/restart`, `/shutdown`, `POST /{id}/screenshot`, `DELETE /{id}/screenshots/{shotId}` |
| `executar` | `POST /{id}/command`, `GET /{id}/shell-check` e `GET /{id}/shell` (esses dois mantêm `sudoGuard`) |
| `ssh` | nenhuma rota; faz a chave SSH do usuário ser instalada na frota (seção 3) |
| `gerenciar` | `POST /pair`, `PATCH /{id}`, `DELETE /{id}`, `/unpair`, `/reset-secret`, CRUD `/hour-lab-expected-programs` |

Nota: `permGuard` não barra token OAuth (`aud`), ao contrário do `adminGuard`. Isso é o que
permite o conector MCP do claude.ai usar essas rotas — é intencional (decisão 3). As demais áreas
continuam `adminGuard` e seguem recusando OAuth.

## 3. SSH por usuário

**Conta alvo:** `santos-fleet` em todo PC (local, Administradores, senha aleatória descartada,
oculta do logon, `Match User santos-fleet` → `administrators_authorized_keys`, só chave). Criada
pelo script `fix-ssh.ps1` (validado no gazake em 25/09; sendo aplicado na frota).

**Chave do usuário:** `users.ssh_public_key TEXT` (uma por usuário). Endpoints
`PUT/DELETE /auth/me/ssh-key` (authGuard; valida com o regex já existente em
`handlers_lab_devices.go:16-20`, só `ssh-ed25519`/`ecdsa`/`rsa ≥ 3072`).

**Distribuição:** o heartbeat passa a devolver `fleetSSHKeys`: lista de
`{id: "st-admin-N" | "st-user-<id>", key}` = `FLEET_ADMIN_SSH_PUBLIC_KEYS` + chaves de usuários com
`dispositivos:ssh` efetivo (admins incluídos). Mesma condição atual: só para dispositivo nomeado.
Consulta com cache Redis de 60s (é por heartbeat de cada PC).

**Aplicação (watchdog, SYSTEM):** novo bloco em `wnsh-loop.ps1` que **sincroniza**
`administrators_authorized_keys`: cada linha gerenciada leva o comentário `st-managed:<id>`;
adiciona as que faltam, **remove** as gerenciadas que não vieram, nunca toca em linhas sem o
marcador. Depois: ACL SYSTEM+Admins, **dono Administradores** (`/setowner`, senão o sshd recusa
com `Bad owner`). Só reescreve se o conteúdo mudou. Não reinicia o sshd (ele relê o arquivo a
cada login).

O `adminSSHPublicKeys` atual (consumido pelo hour-timer-app no perfil do usuário) continua igual
para não quebrar PCs antigos.

## 4. Fila de comandos, auditoria e long-poll

**Tabela** `hour_lab_device_commands`:
`id uuid pk, device_id uuid fk → hour_lab_devices on delete cascade, user_id int fk → users,
source text ('painel'|'mcp_oauth'|'pat'), kind text ('command'|'shell_open'|'shell_close'|
'lock'|'restart'|'shutdown'|'message'), text text, result text, created_at, delivered_at,
result_at`. Índice `(device_id, created_at) WHERE kind='command' AND delivered_at IS NULL`.

Ela vira **a fila** de comandos: `POST /{id}/command` insere (não sobrescreve mais
`hour_lab_devices.command_*` — hoje dois usuários ao mesmo tempo apagam o comando um do outro).
Heartbeat e long-poll entregam **o pendente mais antigo** (`delivered_at IS NULL`), marcando
`delivered_at`; o formato da resposta (`command: {id, text}`) não muda, então o watchdog atual
continua funcionando. `command-result` grava em `result`/`result_at` pelo `commandId`.
Colunas `command_*` antigas: mantidas por uma versão (espelho do último comando) e removidas depois.

`lock/restart/shutdown/message/shell` também gravam linha (kind próprio) só como auditoria.
`source` sai do tipo de token do request (`aud` presente → `mcp_oauth`; prefixo `st_` → `pat`;
cookie de sessão → `painel`).

**Quem executa:** só o watchdog. Heartbeats com `appVersion != "wnsh-watchdog"` não recebem
`command` se o dispositivo teve heartbeat de watchdog nos últimos 5 min. (Hoje o hour-timer-app
também executa, como o usuário logado — causou o `Bad owner` no gazake.)

**Long-poll:** `POST /public/lab-devices/wait-command {deviceId, deviceSecret}` (mesma auth do
heartbeat). Segura até 50s checando a fila a cada 1s; responde na hora com
`{command: {id, text}}` se aparecer, senão `204`. Funciona com N réplicas (estado no banco).
Rate limit 120/min. No `wnsh-loop.ps1`, o `Start-Sleep -Seconds 60` final vira um loop que chama
`wait-command` até completar ~60s, executando o que vier (mesma rotina de execução/resultado já
existente, extraída para uma função). Latência: ~1s normal, até ~20s quando o loop está nas
voltas pesadas.

**Bug junto:** na volta em que o watchdog adota segredo novo, `$deviceSecret` ainda é o antigo
(vazio) e o `command-result` volta 401 — o resultado se perde. Corrigir usando
`$resp.deviceSecret` quando vier.

**Leitura:** `GET /hour-lab-devices/{id}/commands?limit=50` (`dispositivos:ver`) e
`GET /hour-lab-devices/{id}/commands/{cmdId}` (para o MCP buscar resultado).

## 5. MCP (`apps/mcp-go/tools_devices.go`)

Todas via `s.proxy` com o token do usuário; a api-go decide a permissão.

| Tool | Chama |
|---|---|
| `devices_list` | `GET /hour-lab-devices` |
| `device_commands` | `GET /hour-lab-devices/{id}/commands` |
| `device_control {id, action: message|lock|restart|shutdown, text?}` | rota correspondente |
| `device_screenshot {id}` | `POST /{id}/screenshot`, espera até 40s por um print novo, devolve a URL |
| `device_command_run {id, powershell}` | `POST /{id}/command` e espera até 45s pelo resultado (poll de 1s em `/commands/{cmdId}`); se não voltar, devolve `commandId` |
| `device_command_result {id, commandId}` | `GET /{id}/commands/{cmdId}` |

Descrição de `device_command_run` avisa que roda como SYSTEM e que o texto fica auditado.
`WriteTimeout` do mcp-go (60s) comporta os 45s.

## 6. Front (repo `dashboard`)

- `UsuarioDetalhe.tsx`: grade de permissões individuais (reaproveitando o componente de grade
  do `CargoDialog.tsx`), mostrando herdadas do cargo como marcadas-e-travadas. Salvar pede sudo.
- `cargo-constants.ts`: grupo "Laboratório" com `dispositivos: ver, controlar, executar, ssh,
  gerenciar`.
- Perfil: campo "Chave SSH pública".
- `App.tsx`/`nav.ts`: rotas e menu de `/admin/horas/dispositivos` saem de `adminOnly` para
  `permission: dispositivos:ver` (o resto de "horas" segue adminOnly).
- Página do PC: aba "Histórico" (lista de `/commands`), botões escondidos conforme a ação.

## 7. Correções e versionamento incluídos

- `handlers_lab_device_shell.go:177`: `c.CloseRead()` na conexão do agente e depois leitura em
  `relayBinary` — na coder/websocket 1.8.15 isso fecha a sessão na primeira saída. Reproduzir
  ao vivo antes de corrigir (remover o `CloseRead`, a leitura é do relay).
- Versionar `wnsh-loop.ps1` e `collect-apps.ps1` em `infra/fleet/` (fonte da verdade no git; o
  upload pro R2 `downloads/wnsh-loop.ps1` passa a sair dali). `wnsh-shell-agent.exe`: localizar a
  fonte; se não existir, registrar como dívida.
- `CLAUDE.md`: atualizar a seção "[PLANEJADO]" do SSH, que já foi implementado.

## Riscos

- **Execução como SYSTEM via MCP/OAuth:** prompt injection numa conversa pode virar comando na
  frota. Mitigações: permissão explícita por usuário, auditoria com origem, aprovação por tool no
  cliente. Aceito pelo Guilherme.
- **Auto-update do watchdog pelo CDN:** quem escreve em `downloads/wnsh-loop.ps1` no R2 executa
  como SYSTEM em toda a frota. Já era assim; o token R2 continua sendo o ativo mais sensível.
  Fora do escopo, mas registrado.
- **Rollout do watchdog novo:** um erro de sintaxe derruba o heartbeat de todos os PCs de uma
  vez (e com ele o próprio canal de correção). Mitigação: validar com o parser do PowerShell
  antes de subir; subir primeiro com o gazake como canário (SSH de fallback) e só então no R2.

## Ordem de implementação

0. Versionar o watchdog em `infra/fleet/`.
1. Modelo de permissões (seção 1) + testes do `permGuard`/`buildProfile`.
2. Fila/auditoria/long-poll no backend (seção 4) + permissões de dispositivos (seção 2).
3. Watchdog: long-poll + fix do segredo + (depois) sync de chaves; canário no gazake.
4. MCP (seção 5) → Gabriel já usa.
5. SSH por usuário (seção 3).
6. Front (seção 6).
7. Bug do shell (seção 7).

## Padrões do repo que se aplicam

- Todo SQL novo em `apps/api-go/db/query/*.sql` via sqlc (padrão 6 do `CLAUDE.md`), nada inline.
- Rotas novas/alteradas → `docs/openapi.yaml` e `apps/api-go/llms.txt` no mesmo commit.
- Gate antes de commit: `gofmt -l`, `go vet`, `go build`, `go test` (api-go e mcp-go);
  `bun run lint` + `bun run build` no dashboard. `/security-review` nas partes de auth.
