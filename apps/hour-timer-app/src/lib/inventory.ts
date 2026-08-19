import { invoke } from "@tauri-apps/api/core";
import type { Store } from "@tauri-apps/plugin-store";

const INVENTORY_HASH_KEY = "inventoryHash";
const INVENTORY_SENT_AT_KEY = "inventorySentAt";

// Reenvio periódico mesmo sem mudança: prova pro admin que a foto é recente.
// Sem isso, um PC desligado há um mês exibiria a lista antiga como se fosse de
// hoje — e "não sei" é diferente de "está tudo instalado".
const MAX_AGE_MS = 24 * 60 * 60 * 1000;

interface InstalledProgram {
  name: string;
  version: string;
  publisher: string;
}

// Hash barato (djb2) só pra decidir "mudou desde a última vez?". Não é
// segurança: colisão aqui atrasa um envio, não expõe nada.
function hashPrograms(programs: InstalledProgram[]) {
  const text = programs.map((p) => `${p.name}@${p.version}`).join("\n");
  let h = 5381;
  for (let i = 0; i < text.length; i++) h = ((h << 5) + h + text.charCodeAt(i)) | 0;
  return String(h);
}

// Manda a lista de programas instalados quando ela muda (ou uma vez por dia).
// Chamado depois de um heartbeat bem-sucedido: o servidor exige o mesmo
// deviceSecret, então não adianta tentar antes da adoção.
//
// Falha silenciosa de propósito — inventário é informação de apoio pro admin,
// não pode atrapalhar o cronômetro, que é a função do app.
export async function syncInventory(
  store: Store,
  apiOrigin: string,
  deviceId: string,
  deviceSecret: string | null | undefined,
) {
  if (!deviceSecret) return; // PC ainda não adotado
  try {
    const programs = await invoke<InstalledProgram[]>("list_installed_programs");
    if (!programs.length) return;

    const hash = hashPrograms(programs);
    const lastHash = await store.get<string>(INVENTORY_HASH_KEY);
    const lastSentAt = (await store.get<number>(INVENTORY_SENT_AT_KEY)) ?? 0;
    if (hash === lastHash && Date.now() - lastSentAt < MAX_AGE_MS) return;

    const res = await fetch(`${apiOrigin}/public/lab-devices/inventory`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ deviceId, deviceSecret, programs }),
    });
    if (!res.ok) return; // tenta de novo no próximo ciclo

    await store.set(INVENTORY_HASH_KEY, hash);
    await store.set(INVENTORY_SENT_AT_KEY, Date.now());
  } catch {
    // sem rede, registro ilegível, etc. — próximo heartbeat tenta de novo
  }
}
