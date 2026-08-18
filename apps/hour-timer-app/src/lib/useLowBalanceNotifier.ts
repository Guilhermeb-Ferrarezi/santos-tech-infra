import { useEffect, useRef } from "react";
import { isPermissionGranted, requestPermission, sendNotification } from "@tauri-apps/plugin-notification";

const LOW_THRESHOLD_MINUTES = 10;

// Notifica nativamente quando o saldo cruza os limites (uma vez por
// cruzamento, não a cada poll) — "acabando" (<= 10 min) e "esgotado" (<= 0).
// Só a janela principal chama isso (não o overlay), pra não duplicar aviso
// se as duas estiverem abertas ao mesmo tempo.
export function useLowBalanceNotifier(remainingMinutes: number | null, active: boolean) {
  const notifiedLow = useRef(false);
  const notifiedEmpty = useRef(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const granted = (await isPermissionGranted()) || (await requestPermission()) === "granted";
      if (cancelled || !granted) return;
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (remainingMinutes === null || !active) return;

    if (remainingMinutes <= 0) {
      if (!notifiedEmpty.current) {
        notifiedEmpty.current = true;
        sendNotification({ title: "Saldo esgotado", body: "O tempo do cliente acabou — chame o admin pra encerrar ou recarregar." });
      }
    } else {
      notifiedEmpty.current = false;
    }

    if (remainingMinutes <= LOW_THRESHOLD_MINUTES && remainingMinutes > 0) {
      if (!notifiedLow.current) {
        notifiedLow.current = true;
        sendNotification({ title: "Tempo acabando", body: `Restam ${remainingMinutes} min pro cliente.` });
      }
    } else if (remainingMinutes > LOW_THRESHOLD_MINUTES) {
      notifiedLow.current = false;
    }
  }, [remainingMinutes, active]);
}
