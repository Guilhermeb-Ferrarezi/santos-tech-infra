import { useEffect, useRef, useState } from "react";
import { load, type Store } from "@tauri-apps/plugin-store";
import { getVersion } from "@tauri-apps/api/app";

const STORE_FILE = "config.json";
const DEVICE_ID_KEY = "deviceId";
const LAST_MESSAGE_ID_KEY = "lastMessageId";
const TOAST_MESSAGE_KEY = "toastMessage";
const HEARTBEAT_INTERVAL_MS = 30_000;
const API_ORIGIN = "https://api.santos-tech.com";

let storePromise: Promise<Store> | null = null;
function getStore() {
  if (!storePromise) storePromise = load(STORE_FILE, { autoSave: true });
  return storePromise;
}

interface HeartbeatResponse {
  name: string | null;
  unpairRequested: boolean;
  message?: { id: string; text: string };
}

// Identifica este PC pro admin (device_uuid gerado uma vez, persistido em
// disco) e manda heartbeat periódico — devolve o nome atribuído pelo admin
// (ver hour_lab_devices no backend) e entrega os comandos pendentes:
// despairar remoto e aviso na tela (gravado no store como toastMessage; quem
// exibe é a janela toast própria, ver src/toast.tsx e ensure_toast_window em
// lib.rs — funciona mesmo com a janela principal escondida na bandeja). Roda
// só na janela principal (repetir no overlay seria redundante).
export function useDeviceHeartbeat(token: string | null, onUnpairRequested: () => void) {
  const [deviceName, setDeviceName] = useState<string | null>(null);
  const tokenRef = useRef(token);
  tokenRef.current = token;

  useEffect(() => {
    let cancelled = false;

    async function beat() {
      const store = await getStore();
      let deviceId = await store.get<string>(DEVICE_ID_KEY);
      if (!deviceId) {
        deviceId = crypto.randomUUID();
        await store.set(DEVICE_ID_KEY, deviceId);
      }
      const appVersion = await getVersion();

      let res: Response;
      try {
        res = await fetch(`${API_ORIGIN}/public/lab-devices/heartbeat`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ deviceId, token: tokenRef.current, appVersion }),
        });
      } catch {
        return; // sem rede — tenta de novo no próximo ciclo, silencioso
      }
      if (cancelled || !res.ok) return;
      const data: HeartbeatResponse = await res.json();

      setDeviceName(data.name);
      if (data.unpairRequested) onUnpairRequested();

      if (data.message) {
        const lastShownId = await store.get<string>(LAST_MESSAGE_ID_KEY);
        if (data.message.id !== lastShownId) {
          await store.set(LAST_MESSAGE_ID_KEY, data.message.id);
          await store.set(TOAST_MESSAGE_KEY, data.message);
        }
      }
    }

    beat();
    const id = setInterval(beat, HEARTBEAT_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [onUnpairRequested]);

  return { deviceName };
}
