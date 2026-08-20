import { useEffect } from "react";
import { check } from "@tauri-apps/plugin-updater";
import { relaunch } from "@tauri-apps/plugin-process";

const CHECK_INTERVAL_MS = 2 * 60 * 60 * 1000; // 2h

// Checa contra latest.json (ver endpoints em tauri.conf.json, publicado em
// cdn.santos-tech.com/updates/santos-hub/ — processo de release documentado
// na memória do assistente) e, se achar versão nova, baixa+instala+reabre
// sozinho, sem perguntar nada — mesmo mecanismo do hour-timer-app.
export function useAutoUpdate() {
  useEffect(() => {
    let cancelled = false;

    async function runCheck() {
      try {
        const update = await check();
        if (cancelled || !update) return;
        // timeout explícito: o cliente HTTP do updater não tem timeout por
        // padrão — sem isso, uma falha de rede no meio do download trava o
        // check pra sempre em vez de tentar de novo no próximo ciclo.
        await update.downloadAndInstall(undefined, { timeout: 60_000 });
        if (!cancelled) await relaunch();
      } catch {
        // sem rede, endpoint fora do ar, etc. — tenta de novo no próximo ciclo
      }
    }

    runCheck();
    const id = setInterval(runCheck, CHECK_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);
}
