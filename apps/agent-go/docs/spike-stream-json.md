# Spike — protocolo stream-json do CLI claude (motor de sessão viva)

CLI testado: **claude 2.1.195**. Data: 2026-06-26.

Objetivo: validar as premissas do plano `docs/superpowers/plans/2026-06-26-motor-sessao-viva-agent-go.md`
antes de escrever o motor. **Resultado: todas confirmadas — caso A.**

## Achados

### 1. Input stream-json e formato da mensagem
- `--output-format stream-json` com `--print` (`-p`) **exige `--verbose`** (senão: `Error: ... requires --verbose`). O `claudeArgs` atual já passa `--verbose`, então o motor está coberto.
- Formato aceito no stdin (uma linha por mensagem):
  ```json
  {"type":"user","message":{"role":"user","content":[{"type":"text","text":"..."}]}}
  ```
  → confirma `userMessageJSON` (Task 4).
- `--replay-user-messages` re-emite a mensagem do usuário no stream com `"isReplay":true`.

### 2. Processo vivo multi-turno
- Um único processo processa múltiplas mensagens enviadas pelo stdin ao longo do tempo,
  mantendo contexto em memória entre turnos. Cada turno termina com um evento `result`.

### 3. Interrupt sem matar o processo — **CASO A**
- Enviar pelo stdin:
  ```json
  {"type":"control_request","request":{"subtype":"interrupt"},"request_id":"r1"}
  ```
  retorna `{"type":"control_response","response":{"subtype":"success"}}` e **interrompe o turno
  em andamento sem matar o processo**.
- O turno interrompido fecha com `result` de subtype **`error_during_execution`** (não `success`).
- Após o interrupt, o **mesmo processo** processa normalmente a próxima mensagem do usuário.
- Implicação p/ Task 5: `Stop()` escreve o `control_request` no stdin. O `readLoop` detecta fim
  de turno por `type=="result"` (qualquer subtype), então a volta a `idle` já é coberta.
- Implicação p/ o fake (Task 5): ao receber `control_request`, o fake deve emitir um `result`
  (subtype irrelevante para o teste).

### 4. Ressurreição via `--resume` (base da hibernação)
- Processo A grava uma palavra-chave e encerra (stdin fechado). Processo B novo, com
  `--resume <session-id>`, **lembra** a palavra-chave.
- Confirma que fechar o stdin persiste a sessão no disco → hibernar + ressuscitar funciona
  (Tasks 6 e 7).

## Conclusão
O plano segue **sem alterações de premissa**: motor vivo com `--input-format stream-json`,
interrupt limpo via `control_request`, hibernação via fechar-stdin + ressuscitar com `--resume`.
