import { invoke } from "@tauri-apps/api/core";

// Executa o comando livre (PowerShell) pedido pelo admin e manda o resultado
// de volta, correlacionado por commandId (o mesmo id que veio no heartbeat).
// Ao contrário da captura de tela (screenshot.ts), não há aviso na tela do
// PC: quem envia um comando livre já é um admin autenticado que sabe o que
// está fazendo, diferente de uma captura que pode pegar dado pessoal de quem
// está sentado na máquina.
export async function runCommandAndSend(
  apiOrigin: string,
  deviceId: string,
  deviceSecret: string | null | undefined,
  commandId: string,
  text: string,
) {
  if (!deviceSecret) return;

  let result: string;
  try {
    result = await invoke<string>("run_powershell_command", { text });
  } catch (e) {
    // Falha ao executar (plataforma não suportada, powershell.exe ausente) —
    // manda o erro como resultado, pra tela do admin mostrar algo em vez de
    // ficar esperando pra sempre.
    result = `(erro ao executar: ${e})`;
  }

  try {
    await fetch(`${apiOrigin}/public/lab-devices/command-result`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ deviceId, deviceSecret, commandId, result }),
    });
  } catch {
    // Sem rede: o admin vê que o resultado nunca chegou e reenvia o comando.
  }
}
