import { useEffect, useRef } from "react";
import { load, type Store } from "@tauri-apps/plugin-store";

const STORE_FILE = "config.json";
const TOAST_MESSAGE_KEY = "toastMessage";
const LOW_THRESHOLD_MINUTES = 10;

let storePromise: Promise<Store> | null = null;
function getStore() {
  if (!storePromise) storePromise = load(STORE_FILE, { autoSave: true });
  return storePromise;
}

async function pushToast(text: string) {
  const store = await getStore();
  await store.set(TOAST_MESSAGE_KEY, { id: crypto.randomUUID(), text });
}

// Avisa quando o saldo cruza os limites (uma vez por cruzamento, não a cada
// poll) — "acabando" (<= 10 min) e "esgotado" (<= 0) — pelo mesmo popup no
// canto usado pro aviso do admin (ver toast.tsx/ensure_toast_window em
// lib.rs), não mais a notificação nativa do Windows (aparecia como "Windows
// PowerShell", sem identidade nenhuma do app). Só a janela principal chama
// isso (não o overlay), pra não duplicar aviso se as duas estiverem abertas.
export function useLowBalanceNotifier(remainingMinutes: number | null, active: boolean) {
  const notifiedLow = useRef(false);
  const notifiedEmpty = useRef(false);

  useEffect(() => {
    if (remainingMinutes === null || !active) return;

    if (remainingMinutes <= 0) {
      if (!notifiedEmpty.current) {
        notifiedEmpty.current = true;
        pushToast("Saldo esgotado — o tempo do cliente acabou. Chame o colaborador pra encerrar ou recarregar.");
      }
    } else {
      notifiedEmpty.current = false;
    }

    if (remainingMinutes <= LOW_THRESHOLD_MINUTES && remainingMinutes > 0) {
      if (!notifiedLow.current) {
        notifiedLow.current = true;
        pushToast(`Tempo acabando — restam ${remainingMinutes} min pro cliente.`);
      }
    } else if (remainingMinutes > LOW_THRESHOLD_MINUTES) {
      notifiedLow.current = false;
    }
  }, [remainingMinutes, active]);
}
