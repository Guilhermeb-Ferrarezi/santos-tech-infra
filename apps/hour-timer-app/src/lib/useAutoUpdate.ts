import { useEffect } from "react";
import { check } from "@tauri-apps/plugin-updater";
import { relaunch } from "@tauri-apps/plugin-process";

const CHECK_INTERVAL_MS = 2 * 60 * 60 * 1000; // 2h — PC de laboratório fica sempre ligado

// PC de laboratório: ninguém vai em cada máquina reinstalar a cada release.
// Checa contra latest.json (ver endpoints em tauri.conf.json, publicado em
// cdn.santos-tech.com/updates/hour-timer-app/ — processo de release descrito
// na memória do assistente) e, se achar versão nova, baixa+instala+reabre
// sozinho, sem perguntar nada — mesmo espírito do watchdog (sempre rodando,
// sempre atualizado, sem depender de alguém mexer no PC). timeout explícito
// no download: o cliente HTTP do updater não tem timeout por padrão — sem
// isso, uma falha de rede no meio do download trava o check pra sempre em
// vez de tentar de novo no próximo ciclo.
export function useAutoUpdate() {
  useEffect(() => {
    let cancelled = false;

    async function runCheck() {
      try {
        const update = await check();
        if (cancelled || !update) return;
        await update.downloadAndInstall(undefined, { timeout: 60_000 });
        if (!cancelled) await relaunch();
      } catch {
        // sem rede, endpoint fora do ar, download travou, etc. — tenta de
        // novo no próximo ciclo, silencioso (PC de laboratório sem ninguém
        // olhando a tela pra tratar erro)
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
