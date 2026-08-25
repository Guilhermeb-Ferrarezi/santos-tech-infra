import { useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { load, type Store } from "@tauri-apps/plugin-store";
import { getVersion } from "@tauri-apps/api/app";
import { syncInventory } from "./inventory";
import { captureAndSend } from "./screenshot";

const STORE_FILE = "config.json";
const DEVICE_ID_KEY = "deviceId";
// Identidade anterior, guardada em disco até o servidor confirmar que não sobrou
// nada a migrar. Ver o bloco em beat() — mandar uma vez só não basta.
const PREVIOUS_DEVICE_ID_KEY = "previousDeviceId";
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
  /** O admin pediu uma captura de tela (entregue uma única vez). */
  screenshotRequested?: boolean;
  /** Chave(s) pública(s) do admin da frota — instaladas no authorized_keys deste PC (ver sync_authorized_keys). */
  adminSSHPublicKeys?: string[];
  // Credencial deste PC, emitida pelo servidor UMA ÚNICA VEZ (no primeiro
  // heartbeat de um device_uuid ainda sem segredo). Guardamos em disco e
  // reenviamos em todo heartbeat seguinte — sem ela o servidor responde 401 e
  // não entrega comando nem pairToken. Se o disco for perdido, o admin usa
  // POST /hour-lab-devices/{id}/reset-secret pra permitir nova adoção.
  deviceSecret?: string;
  /** Não sobrou nada a migrar do previousDeviceId — pode esquecê-lo. */
  previousDeviceResolved?: boolean;
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
      const storedId = await store.get<string>(DEVICE_ID_KEY);

      // Identidade derivada do MachineGuid do Windows (0.1.10+): sobrevive a
      // reinstalação do app e é a mesma em qualquer conta de usuário da
      // máquina. Antes o id era sorteado e guardado só aqui — perder o
      // config.json fazia o mesmo PC virar dois dispositivos no dashboard.
      let stableId = "";
      try {
        stableId = await invoke<string>("stable_device_id");
      } catch {
        // registro ilegível: segue com o id do store (comportamento antigo)
      }

      const deviceId = stableId || storedId || crypto.randomUUID();
      // previousDeviceId manda o id ANTIGO junto pro servidor MOVER o registro
      // (nome, sessão, inventário, capturas) em vez de criar um duplicado.
      //
      // Fica gravado em disco e vai em TODO heartbeat até o servidor responder
      // previousDeviceResolved. Mandar uma vez só não funciona: no rollout da
      // 0.1.10 o primeiro heartbeat chegou 6 segundos antes de a API nova subir,
      // o servidor antigo ignorou o campo, e o PC virou dois dispositivos sem
      // chance de retomada — o app já tinha esquecido a identidade anterior.
      // Insistindo, a migração acontece sozinha quando o impedimento sai do
      // caminho (deploy termina, admin esquece o segredo do registro antigo ou
      // apaga a duplicata).
      if (deviceId !== storedId) {
        if (storedId) await store.set(PREVIOUS_DEVICE_ID_KEY, storedId);
        await store.set(DEVICE_ID_KEY, deviceId);
      }
      const previousDeviceId = (await store.get<string>(PREVIOUS_DEVICE_ID_KEY)) || undefined;
      if (!cancelled) setDeviceId(deviceId);
      const deviceSecret = await store.get<string>(DEVICE_SECRET_KEY);
      const appVersion = await getVersion();

      // Aplicativos abertos AGORA, pelo nome — o admin vê no dashboard o que a
      // máquina está rodando. Vai junto do heartbeat porque é dado que só vale
      // fresco: pedir sob demanda daria uma foto de 30s atrás do mesmo jeito.
      // Falha aqui não pode derrubar o heartbeat, que é a função principal.
      let openApps: string[] = [];
      try {
        openApps = await invoke<string[]>("list_open_apps");
      } catch {
        // sem lista: o servidor mantém a última conhecida
      }

      // Provisionamento de chave SSH (ver CLAUDE.md raiz, bloco
      // hour-timer-app): no primeiro startup sem chave, o comando Rust gera o
      // par e devolve a pública; nos seguintes já existe e só lê. String
      // vazia (chave ilegível/erro) é a semântica que o servidor já entende
      // como "não mudar o que está guardado" — não precisa de retry aqui.
      let sshPublicKey = "";
      try {
        sshPublicKey = await invoke<string>("ensure_ssh_public_key");
      } catch {
        // sem chave: heartbeat segue sem ela, servidor mantém a já registrada
      }

      let res: Response;
      try {
        res = await fetch(`${API_ORIGIN}/public/lab-devices/heartbeat`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ deviceId, deviceSecret, token: tokenRef.current, appVersion, openApps, previousDeviceId, sshPublicKey }),
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

      // Migração concluída (ou nada a migrar): para de mandar o id antigo.
      if (previousDeviceId && data.previousDeviceResolved) {
        await store.delete(PREVIOUS_DEVICE_ID_KEY);
      }

      // Inventário de software (o admin vê em /admin/horas/dispositivos): só
      // sai quando a lista muda ou passa um dia, e nunca antes da adoção — a
      // rota exige o mesmo segredo. Não é await pra não segurar o heartbeat.
      void syncInventory(store, API_ORIGIN, deviceId, data.deviceSecret ?? deviceSecret);

      // Captura de tela pedida pelo admin: tira a foto, avisa na tela do PC e
      // manda. Sob demanda e uma vez por pedido — nunca em laço.
      if (data.screenshotRequested) {
        void captureAndSend(store, API_ORIGIN, deviceId, data.deviceSecret ?? deviceSecret);
      }

      // Instala a(s) chave(s) do admin no authorized_keys — é isso que
      // habilita `ssh` de fora pra dentro deste PC (ver sync_authorized_keys
      // em lib.rs; ensure_ssh_public_key acima é só a identidade do PC, não
      // dá acesso a ninguém). Roda em todo heartbeat, não só na adoção: se a
      // chave rodar, o PC converge sozinho sem precisar reinstalar o app.
      // Falha aqui não pode derrubar o heartbeat.
      if (data.adminSSHPublicKeys?.length) {
        invoke("sync_authorized_keys", { keys: data.adminSSHPublicKeys }).catch(() => {
          // sem escrita: tenta de novo no próximo heartbeat
        });
      }

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
