import { useEffect, useState } from "react";

// Cronômetro suave: a fonte de verdade continua vindo do poll (a cada
// poucos segundos), mas a exibição soma +1s localmente entre um poll e
// outro — sem precisar de SSE/WebSocket. `base` guarda o último valor visto
// do servidor; quando ele muda, o estado é resetado durante o próprio
// render (padrão documentado do React pra ajustar estado a partir de
// props, sem passar por useEffect) e o interval segue contando a partir
// dali. Fica parado no valor do servidor enquanto `active` é falso (sessão
// pausada/encerrada). Espelha dashboard/web/src/lib/hour-sessions/useTickingSeconds.ts.
export function useTickingSeconds(serverElapsedSeconds: number, active: boolean) {
  const [state, setState] = useState({ base: serverElapsedSeconds, display: serverElapsedSeconds });
  if (state.base !== serverElapsedSeconds) {
    setState({ base: serverElapsedSeconds, display: serverElapsedSeconds });
  }

  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setState((s) => ({ ...s, display: s.display + 1 })), 1_000);
    return () => clearInterval(id);
  }, [active]);

  return active ? state.display : serverElapsedSeconds;
}
