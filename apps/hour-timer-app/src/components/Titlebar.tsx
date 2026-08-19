import { Minus, X } from "@phosphor-icons/react";
import { getCurrentWindow } from "@tauri-apps/api/window";

const appWindow = getCurrentWindow();

// Titlebar custom (decorations:false em tauri.conf.json) — a barra branca
// nativa do Windows destoava do fundo escuro do app. "Fechar" só esconde: o
// CloseRequested handler em lib.rs intercepta e manda pra bandeja.
export function Titlebar({ deviceName }: { deviceName?: string | null }) {
  return (
    <div
      data-tauri-drag-region
      className="flex h-8 shrink-0 items-center justify-between bg-[#0E2937] pl-3 text-white select-none"
    >
      <p data-tauri-drag-region className="truncate text-xs font-semibold text-white/80">
        Santos Tech{deviceName ? ` · ${deviceName}` : ""}
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
          title="Fechar (continua na bandeja)"
          className="flex h-full w-10 items-center justify-center text-white/60 transition-colors hover:bg-red-500 hover:text-white"
        >
          <X className="size-3.5" />
        </button>
      </div>
    </div>
  );
}
