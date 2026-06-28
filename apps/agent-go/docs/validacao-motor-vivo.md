# Validação do motor de sessão viva (Task 9)

Data: 2026-06-28. CLI: `claude` 2.1.195.

## Cobertura

A validação tem duas camadas:

1. **Motor contra o CLI real** — `e2e_live_test.go` (build tag `e2e_live`). Exercita o
   motor vivo de verdade contra o `claude` instalado, sem Postgres/Redis (a persistência
   é nil-guarded). Cobre os cenários da Task 9 no nível do motor.
2. **HTTP + auth + DB + WS ponta-a-ponta** — **FEITO** (2026-06-28, ver resultados no fim).
   Rodado com Postgres + Redis (containers), JWT admin forjado e o `claude` real.

## Resultado — e2e contra o CLI real (3/3 PASS)

```
go test -tags e2e_live -run TestE2ELive -v -timeout 12m
--- PASS: TestE2ELiveSemColdStartEFila      (multi-turno, mesmo processo, sem cold start)
--- PASS: TestE2ELiveStopMantemProcessoVivo (interrupt via control_request; processo vivo)
--- PASS: TestE2ELiveHibernacaoRessurreicao (close → --resume preserva a memória)
ok  github.com/santos-tech/agent  16.8s
```

- **Sessão viva / sem cold start:** 3 turnos (UM, DOIS, TRÊS) no MESMO processo vivo;
  `m.live[conv.ID]` reusado entre turnos (sem respawn).
- **Stop:** turno longo interrompido por `control_request`; `ls.done` NÃO fecha (processo
  sobrevive) e um turno seguinte responde normalmente.
- **Hibernação → ressurreição:** após o 1º turno, `SessionStarted=true`; `close()` encerra
  o processo e `removeLive` o tira do pool; novo `ensureLive` sobe um processo NOVO com
  `--resume` que **lembra a palavra-chave** (ROXO-42) — memória preservada via disco.

> A **fila** concorrente (mensagem durante um turno) é coberta deterministicamente pelo
> teste unitário `TestLiveSessionDoisTurnosMesmoProcesso` (fake claude + `FAKE_DELAY_MS`);
> contra o CLI real o timing não é controlável (turnos rápidos não se sobrepõem).

## Semântica de eventos confirmada
- Cada turno emite um `result`. O `done` é emitido **uma vez**, quando a fila esvazia e a
  sessão volta a idle — não por turno. (Clientes que pipelineiam devem usar `result` por
  turno e tratar `done` como "ocioso".)

## Como rodar
```bash
export PATH="$HOME/.local/bin:$PATH"
cd apps/agent-go
go test -tags e2e_live -run TestE2ELive -v -timeout 12m   # requer claude logado
```

## Achado durante a validação
`oauthToken` (chamado por `claudeEnv`) não tinha nil-guard — os testes com fake nunca o
exercitavam (injetam `testEnv`, pulam `claudeEnv`). Em produção `db` nunca é nil, mas o
guard foi adicionado por consistência e para permitir rodar o motor sem DB (usa as
credenciais locais `~/.claude`, como no fluxo logged_out).

## e2e HTTP completo — resultados (2026-06-28)

Ambiente: Postgres + Redis (containers standalone, portas no host), `users(id=1, role=3)`
criado antes do boot (FK de `claude_conversations`), JWT HS256 `sub=1` forjado com o
`JWT_SECRET`, `CLAUDE_BIN=claude` real, `CLAUDE_IDLE_TTL=20s`. Cliente WS em Node (`ws`).

Todos os cenários **PASS**:
- **Boot**: `/claude/ready` → `{postgres:ok, redis:ok}`; `migrate()` criou as tabelas
  `claude_*` (FK `users` satisfeita).
- **Auth + DB**: `POST /claude/conversations` com Bearer JWT admin → conversa criada
  (`userId:1`, workdir gerado).
- **Motor vivo via WS**: 2 prompts no MESMO socket → "UM", "DOIS" (2º turno com menos
  eventos `init` = sem cold start). Após o WS fechar, o processo
  `claude … --input-format stream-json --session-id …` **continua vivo**.
- **Persistência**: `claude_messages` gravou user/system/assistant/result dos 2 turnos;
  conversa `status=idle`, `session_started=t`.
- **Roteamento WhatsApp**: conversa `toolsDisabled=true` respondeu via por-turno — **não**
  criou processo vivo (contagem de `--input-format stream-json` não subiu) e o processo
  por-turno morreu após o turno.
- **Hibernação + ressurreição**: após `CLAUDE_IDLE_TTL`, o reaper logou
  "hibernando sessão viva ociosa" e o `close()` **terminou o processo** (sem leak, poll
  confirmou); o prompt seguinte ressuscitou com `--resume <session_id>` e respondeu.

Reproduzir: subir Postgres+Redis (ex.: containers com portas no host), criar `users`
(`id` BIGINT, `role` SMALLINT=3) ANTES do boot, exportar `DATABASE_URL`/`REDIS_URL`/
`JWT_SECRET`/`ENCRYPTION_KEY`/`CLAUDE_BIN=claude`/`PORT`, rodar o binário, forjar um JWT
`sub=<id>` e exercitar `/claude/conversations` + o WS `/conversations/{id}/ws`.
