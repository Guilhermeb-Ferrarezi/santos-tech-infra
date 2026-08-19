import { Minus, X } from "@phosphor-icons/react";
import { getCurrentWindow } from "@tauri-apps/api/window";

const appWindow = getCurrentWindow();

// Titlebar custom (decorations:false em tauri.conf.json) — a barra branca
// nativa do Windows destoava do fundo escuro do app. Diferente do
// hour-timer-app, aqui "Fechar" encerra o processo de verdade — o Santos Hub
// não roda em segundo plano, é só um launcher que se abre quando precisa.
export function Titlebar() {
  return (
    <div
      data-tauri-drag-region
      className="flex h-8 shrink-0 items-center justify-between bg-[#0E2937] pl-3 text-white select-none"
    >
      <p data-tauri-drag-region className="truncate text-xs font-semibold text-white/80">
        Santos Hub
      </p>
      <div className="flex h-full">
        <button
          type="button"
          onClick={() => appWindow.minimize()}
          title="Minimizar"
          className="flex h-full w-10 items-center justify-center text-white/60 transition-colors hover:bg-white/10 hover:text-white"
        >
          <Minus className="size-3.5" />
        </button>
        <button
          type="button"
          onClick={() => appWindow.close()}
          title="Fechar"
          className="flex h-full w-10 items-center justify-center text-white/60 transition-colors hover:bg-red-500 hover:text-white"
        >
          <X className="size-3.5" />
        </button>
      </div>
    </div>
  );
}
