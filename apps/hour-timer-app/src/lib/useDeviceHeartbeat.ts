import { useEffect, useRef, useState } from "react";
import { load, type Store } from "@tauri-apps/plugin-store";
import { getVersion } from "@tauri-apps/api/app";
import { syncInventory } from "./inventory";

const STORE_FILE = "config.json";
const DEVICE_ID_KEY = "deviceId";
const DEVICE_SECRET_KEY = "deviceSecret";
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
  pairToken?: string;
  // Credencial deste PC, emitida pelo servidor UMA ÚNICA VEZ (no primeiro
  // heartbeat de um device_uuid ainda sem segredo). Guardamos em disco e
  // reenviamos em todo heartbeat seguinte — sem ela o servidor responde 401 e
  // não entrega comando nem pairToken. Se o disco for perdido, o admin usa
  // POST /hour-lab-devices/{id}/reset-secret pra permitir nova adoção.
  deviceSecret?: string;
}

// Identifica este PC pro admin (device_uuid gerado uma vez, persistido em
// disco) e manda heartbeat periódico — devolve o nome atribuído pelo admin
// (ver hour_lab_devices no backend) e entrega os comandos pendentes:
// despairar remoto, aviso na tela (gravado no store como toastMessage; quem
// exibe é a janela toast própria, ver src/toast.tsx e ensure_toast_window em
// lib.rs — funciona mesmo com a janela principal escondida na bandeja) e
// pairToken (pareamento via QR — admin escaneia o QR desta tela, escolhe o
// cliente em /admin/horas/parear/:deviceUuid, e o token chega aqui sozinho).
// Roda só na janela principal (repetir no overlay seria redundante).
export function useDeviceHeartbeat(token: string | null, onUnpairRequested: () => void, onPaired: (token: string) => void) {
  const [deviceName, setDeviceName] = useState<string | null>(null);
  const [deviceId, setDeviceId] = useState<string | null>(null);
  // true até o primeiro heartbeat falhar — usado pelo ícone da bandeja pra
  // mostrar "sem conexão" (ver TimerScreen/App, comando update_tray_status).
  const [heartbeatOk, setHeartbeatOk] = useState(true);
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
      if (!cancelled) setDeviceId(deviceId);
      const deviceSecret = await store.get<string>(DEVICE_SECRET_KEY);
      const appVersion = await getVersion();

      let res: Response;
      try {
        res = await fetch(`${API_ORIGIN}/public/lab-devices/heartbeat`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ deviceId, deviceSecret, token: tokenRef.current, appVersion }),
        });
      } catch {
        if (!cancelled) setHeartbeatOk(false);
        return; // sem rede — tenta de novo no próximo ciclo, silencioso
      }
      if (cancelled) return;
      if (!res.ok) {
        setHeartbeatOk(false);
        return;
      }
      setHeartbeatOk(true);
      const data: HeartbeatResponse = await res.json();

      // Adoção: o servidor só emite o segredo uma vez, no primeiro heartbeat
      // deste device_uuid. Se não gravarmos agora, os heartbeats seguintes
      // levam 401 e o PC deixa de receber comandos e pairToken.
      if (data.deviceSecret) await store.set(DEVICE_SECRET_KEY, data.deviceSecret);

      // Inventário de software (o admin vê em /admin/horas/dispositivos): só
      // sai quando a lista muda ou passa um dia, e nunca antes da adoção — a
      // rota exige o mesmo segredo. Não é await pra não segurar o heartbeat.
      void syncInventory(store, API_ORIGIN, deviceId, data.deviceSecret ?? deviceSecret);

      setDeviceName(data.name);
      if (data.unpairRequested) onUnpairRequested();
      if (data.pairToken) onPaired(data.pairToken);

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
  }, [onUnpairRequested, onPaired]);

  return { deviceName, deviceId, heartbeatOk };
}
