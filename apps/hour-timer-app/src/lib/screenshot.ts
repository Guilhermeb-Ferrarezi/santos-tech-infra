import { invoke } from "@tauri-apps/api/core";
import type { Store } from "@tauri-apps/plugin-store";

const TOAST_MESSAGE_KEY = "toastMessage";

interface ScreenCapture {
  jpeg: string;
  width: number;
  height: number;
}

// Texto do aviso que aparece na tela do PC quando uma captura acontece.
// A janela de toast é a mesma dos avisos do admin (ver toast.tsx).
const NOTICE = "O administrador visualizou a tela deste computador.";

// Tira a foto da tela e manda pro servidor, DEPOIS de avisar quem está no PC.
//
// A ordem importa: o aviso é gravado antes do envio, então mesmo que a rede
// caia no meio a pessoa sentada na máquina fica sabendo que a tela foi
// capturada. Captura silenciosa num PC de uso compartilhado é o que este
// aviso existe pra impedir.
export async function captureAndSend(
  store: Store,
  apiOrigin: string,
  deviceId: string,
  deviceSecret: string | null | undefined,
) {
  if (!deviceSecret) return;
  try {
    await store.set(TOAST_MESSAGE_KEY, {
      // id novo a cada captura: o toast deduplica por id e precisa reaparecer
      // toda vez que alguém olhar a tela.
      id: `screenshot-${Date.now()}`,
      text: NOTICE,
    });

    const shot = await invoke<ScreenCapture>("capture_screen");
    await fetch(`${apiOrigin}/public/lab-devices/screenshot`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        deviceId,
        deviceSecret,
        jpeg: shot.jpeg,
        width: shot.width,
        height: shot.height,
      }),
    });
  } catch {
    // Sem captura possível (sessão bloqueada, monitor ausente) ou sem rede: o
    // admin vê que a imagem não chegou e pede de novo. Nada aqui pode
    // atrapalhar o cronômetro, que é a função do app.
  }
}
