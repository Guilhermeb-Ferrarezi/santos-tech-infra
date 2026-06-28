# Validação do motor de sessão viva (Task 9)

Data: 2026-06-28. CLI: `claude` 2.1.195.

## Cobertura

A validação tem duas camadas:

1. **Motor contra o CLI real** — `e2e_live_test.go` (build tag `e2e_live`). Exercita o
   motor vivo de verdade contra o `claude` instalado, sem Postgres/Redis (a persistência
   é nil-guarded). Cobre os cenários da Task 9 no nível do motor.
2. **HTTP + auth + DB + WS ponta-a-ponta** — **PENDENTE**. Requer subir o serviço com
   Postgres + Redis + OAuth do Claude + usuário admin (role=3). Não rodável neste ambiente
   (sem Docker/Postgres/Redis). A rodar pelo usuário num ambiente com a infra.

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

## Pendente (e2e HTTP completo)
Subir `docker compose -f infra/docker-compose.yml up -d postgres redis`, criar a tabela
`users` + um usuário role=3, rodar `go run .`, e exercitar via WS (wscat/agent-mobile):
abrir conversa, 2 prompts seguidos (confirmar ausência de cold start no 2º), fila + botão
parar, e ociosidade > `CLAUDE_IDLE_TTL` (hibernação + ressurreição).
