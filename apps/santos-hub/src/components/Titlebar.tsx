import { useEffect, useMemo, useState } from "react";
import { CornersIn, CornersOut, Minus, X } from "@phosphor-icons/react";
import { getCurrentWindow } from "@tauri-apps/api/window";

// Titlebar custom (decorations:false em tauri.conf.json) — a barra branca
// nativa do Windows destoava do fundo escuro do app. Diferente do
// hour-timer-app, aqui "Fechar" encerra o processo de verdade — o Santos Hub
// não roda em segundo plano, é só um launcher que se abre quando precisa.
export function Titlebar() {
  // getCurrentWindow() lê window.__TAURI_INTERNALS__ — chamando só dentro do
  // componente (não no escopo do módulo) garante que só roda depois que o
  // React já montou, nunca antes da ponte do Tauri estar pronta.
  const appWindow = useMemo(() => getCurrentWindow(), []);
  const [maximized, setMaximized] = useState(false);

  useEffect(() => {
    appWindow.isMaximized().then(setMaximized);
    // dobro clique na drag region já maximiza/restaura sozinho (comportamento
    // nativo do Tauri) — só precisamos escutar o resize pra manter o ícone
    // do botão em sincronia com esse gesto.
    const unlisten = appWindow.onResized(() => {
      appWindow.isMaximized().then(setMaximized);
    });
    return () => {
      unlisten.then((fn) => fn());
    };
  }, []);

  return (
    <div
      data-tauri-drag-region
      onDoubleClick={() => appWindow.toggleMaximize()}
      className="flex h-8 shrink-0 items-center justify-between border-b border-white/10 bg-neutral-900 pl-3 text-white select-none"
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
          onClick={() => appWindow.toggleMaximize()}
          title={maximized ? "Restaurar" : "Maximizar"}
          className="flex h-full w-10 items-center justify-center text-white/60 transition-colors hover:bg-white/10 hover:text-white"
        >
          {maximized ? <CornersIn className="size-3.5" /> : <CornersOut className="size-3.5" />}
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
