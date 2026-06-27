# Motor de Sessão Viva — agent-go (Fatia 1)

- **Data:** 2026-06-26
- **App:** `apps/agent-go`
- **Branch:** `feat/agent-go-motor-sessao-viva`
- **Status:** design aprovado, aguardando plano de implementação

## Contexto e problema

O `agent-go` orquestra o Claude Code dentro de um container e expõe um chat por
WebSocket. Hoje o motor é **spawn-por-turno**: cada mensagem do usuário dispara um
processo novo `claude -p --resume <session-id>`, que **re-hidrata a sessão do disco**,
executa **um único turno** e **morre**. O WebSocket transmite os deltas ao vivo
*dentro* do turno, mas **entre uma mensagem e outra não existe nenhum processo Claude
vivo** — a continuidade é uma ilusão costurada pelo `--resume` lendo o `.jsonl` do disco.

Consequência: a experiência é, de fato, request/response. Não há "conexão real" com um
Claude que vive no container, não há como interagir enquanto ele trabalha, e cada turno
paga o custo de cold start (re-hidratar sessão, reinicializar MCP).

## Objetivo desta fatia

Trocar o motor de **spawn-por-turno** por um **motor de sessão viva**: um processo
`claude` **de longa duração por conversa**, alimentado por input em streaming. Isso
destrava de uma vez os dois primeiros pilares da visão do produto:

1. **Sessão viva contínua** — o Claude fica "ligado" na conversa, sem reiniciar a cada
   mensagem, mantendo estado em memória e sem cold start entre turnos.
2. **Interação no meio do trabalho** — é possível mandar mensagem / parar enquanto ele
   já está trabalhando.

### Fora de escopo (fatias futuras)

- Camada "ambiente vivo tipo Replit": workspace navegável, preview do app rodando ao
  vivo, deploy, multiplayer. Cada um vira spec própria **em cima** deste motor.
- Cliente novo (desktop/web/mobile). Esta fatia entrega **só o backend**, validado por
  script. Os clientes atuais (ex.: `agent-mobile`) continuam funcionando por
  retrocompatibilidade do protocolo WS.
- Terminal interativo espelhado (PTY). Explicitamente descartado — o motor usa
  `stream-json`, não PTY.

### Escala alvo

Uso interno (você + poucos admins), **1–5 sessões vivas simultâneas**, tudo no container
atual do `agent-go`. Sem orquestração externa (k8s/Firecracker). Pool de processos em
memória.

## Viabilidade técnica (verificada)

CLI `claude` **2.1.195** suporta o modo necessário:

- `--input-format stream-json` — input em tempo real: o processo fica vivo lendo
  mensagens JSON do stdin (uma por linha) em vez de ler um prompt e sair.
- `--output-format stream-json --include-partial-messages` — o stream de eventos ao vivo
  já consumido hoje.
- `--replay-user-messages` — o CLI re-emite as mensagens do usuário de volta no stream
  (confirmação de recebimento/ordem).

É o mesmo mecanismo que o Claude Agent SDK usa por baixo. **Não precisa de PTY.**

## Design

### Conceito central

Um **`liveSession` por conversa** encapsula um processo `claude` de longa duração:

```
claude -p --input-format stream-json --output-format stream-json \
  --include-partial-messages --replay-user-messages \
  --dangerously-skip-permissions --model <m> --add-dir <workdir> \
  [--mcp-config <workdir>/.mcp.json --strict-mcp-config] \
  (--session-id <sid>   ← processo novo de conversa nova
   | --resume <sid>)    ← ressuscitando conversa hibernada
```

Dentro de um processo vivo, **turnos seguintes não usam flag nenhuma** — apenas
escrevemos outra mensagem no stdin:

```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"…"}]}}
```

O `--resume` deixa de ser "a cada mensagem" e passa a ser **somente o mecanismo de
acordar uma sessão hibernada**.

### Ciclo de vida

- **`ensureLive(conv)`** — se há processo vivo no pool, reusa; senão faz spawn
  (`--session-id` se a conversa é nova, `--resume` se já tem `session_started`).
- **Enviar mensagem** — estado `idle` → marca `running`, escreve no stdin. Estado
  `running` → **enfileira**.
- **Fim de turno** — o evento `result` do stream marca volta a `idle`; se a fila tiver
  mensagens, desenfileira e envia a próxima.
- **Parar (interrupt)** — interrompe o **turno** sem matar o **processo** (volta a
  `idle`). Ver risco #1.
- **Hibernar** — goroutine *reaper* periódica: se `idle` + sem WS conectado + ocioso há
  mais que `idleTTL`, fecha o stdin (o CLI sai limpo e salva a sessão no disco) e remove
  do pool. A próxima mensagem ressuscita via `--resume`.
- **Crash** — processo morto sozinho (erro): marca conversa em erro, emite evento, remove
  do pool. Próxima mensagem ressuscita.
- **Shutdown do servidor** — fecha o stdin de todas as sessões vivas (graceful), aguarda,
  e mata o grupo de processos se travar.

### Concorrência

O semáforo global `turnSlots` (hoje limita 4 turnos concorrentes) é **substituído** por
um **cap de pool de sessões vivas** (`CLAUDE_MAX_LIVE`, default 4). Como cada sessão
processa 1 turno por vez, o cap de pool já é o limite natural de concorrência. Pool cheio
+ chega mensagem para uma conversa sem processo vivo → **evict da LRU ociosa** (hiberna)
para abrir vaga.

### Protocolo WebSocket — retrocompatível

O cliente continua enviando `{type:"prompt", text}` e `{type:"interrupt"}`; os eventos
atuais (`init`, `delta`, `tool_use`, `tool_result`, `result`, `error`, `busy`, `done`,
`title`) seguem iguais.

- `prompt` → `liveSession.Send()` (enfileira se ocupado).
- `interrupt` → `liveSession.Stop()` (para o turno, mantém o processo vivo).
- `done` muda de semântica internamente (fim de turno, não morte do processo) — **o
  cliente não percebe diferença**.
- **Adições opcionais** para UX: evento `queued` (a mensagem entrou na fila) e `state`
  (idle/running). `agent-mobile` continua funcionando sem alteração.

### Persistência

Sem mudança estrutural. `handleEvent` continua persistindo cada evento; a diferença é que
o loop de leitura roda pela **vida do processo**, não por turno. Mensagens enfileiradas
são persistidas no momento do **envio efetivo** (desenfileiramento), preservando a ordem
correta do transcript.

### Mudanças no código

- **`session.go`** — refactor principal: novo tipo `liveSession`,
  `SessionManager.live map[convID]*liveSession`, loop de leitura contínuo (vida do
  processo, não do turno), fila de mensagens, *reaper* de idle. `RunTurn` vira `Send`
  não-bloqueante.
- **`handlers_control.go`** — `/clear` e `/compact` rotacionam o `session_id` → precisam
  **matar e reabrir** o processo vivo da conversa.
- **`ws.go`** — ajustes mínimos (prompt→`Send`, interrupt→`Stop`).
- **Reaproveitado quase inteiro:** `claudeArgs` (ganha só os flags de input),
  `claudeEnv`, `handleEvent`, `Subscribe`/`dispatch`, toda a camada de persistência.

## Fronteira com `claude-remote`

O `claude-remote` (Claude Code Desktop estilo Happy, motor Go na VPS) também terá um
"motor de Claude vivo na nuvem", criando risco de duplicação. **Decisão (YAGNI):** para
uso interno, **não** compartilhar código agora. O motor nasce **dentro do `agent-go`**,
mas isolado num módulo bem-bounded (`liveSession` com interface limpa), de modo a ser
**extraível** para um package compartilhado *se e quando* o `claude-remote` precisar.
Construir a abstração compartilhada antes do segundo consumidor é over-engineering.

## Riscos / itens a verificar empiricamente (CLI 2.1.195)

1. **🔴 MAIOR RISCO — interromper um turno sem matar o processo.** Se o CLI aceitar um
   `control_request` de `interrupt` pelo stdin, ótimo. **Se não**, o "parar" degrada para
   *matar o processo + ressuscitar via `--resume`* (funciona, mas perde o estado em
   memória daquele turno). Aceitável como fallback, mas **deve ser testado num spike
   ANTES de escrever o motor inteiro** — define a robustez do recurso.
2. Formato exato da mensagem `user` aceita no stdin em modo stream-json.
3. Confirmar que fechar o stdin faz o CLI **salvar a sessão no disco** (a ressurreição
   via `--resume` depende disso).
4. `result` marca o fim de turno de forma confiável no modo contínuo.

## Plano de validação (por script, sem cliente novo)

- **Prova de sessão viva:** 2 prompts seguidos no mesmo WS → o 2º **sem cold start** (sem
  re-`init`, latência baixa).
- **Fila:** prompt + outro durante o turno → o 2º roda depois do 1º.
- **Parar:** prompt + interrupt → parou e o processo **continua vivo** (3º prompt
  funciona).
- **Hibernação:** ocioso > `idleTTL` → processo morre; próximo prompt ressuscita via
  `--resume`.
- **Unitários:** fila e pool testados com um *fake claude* (script que ecoa stream-json),
  sem precisar do CLI real nem de RAM.

## Sequência de implementação recomendada

1. **Spike do risco #1** (≈30 min): confirmar o mecanismo de interrupt no modo
   stream-json. O resultado decide o desenho do `Stop()`.
2. Implementar `liveSession` + pool + loop de leitura contínuo (com *fake claude* nos
   testes unitários).
3. Fila + transições de estado (idle/running) + `Send`/`Stop`.
4. Reaper de idle + ressurreição via `--resume`.
5. Adaptar `handlers_control.go` (`/clear`, `/compact`) e `ws.go`.
6. Scripts de validação ponta-a-ponta contra o CLI real.
7. Pré-commit obrigatório do `agent-go`: `gofmt -l .` (vazio) · `go vet ./...` ·
   `go build ./...` · `go test ./...`.
