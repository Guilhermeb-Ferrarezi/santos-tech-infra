import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { load, type Store } from "@tauri-apps/plugin-store";
import { PictureInPicture } from "@phosphor-icons/react";

const STORE_FILE = "config.json";
const OVERLAY_CORNER_KEY = "overlayCorner";

let storePromise: Promise<Store> | null = null;
function getStore() {
  if (!storePromise) storePromise = load(STORE_FILE, { autoSave: true });
  return storePromise;
}

type Corner = "top-left" | "top-right" | "bottom-left" | "bottom-right";

const CORNERS: { value: Corner; label: string }[] = [
  { value: "top-left", label: "Superior esquerdo" },
  { value: "top-right", label: "Superior direito" },
  { value: "bottom-left", label: "Inferior esquerdo" },
  { value: "bottom-right", label: "Inferior direito" },
];

// Substitui o antigo botão "Pedir pausa" — configura em qual canto da tela o
// widget flutuante do cronômetro (HUD, ver overlay.tsx/toggle_overlay em
// lib.rs) fica. Grava no mesmo store lido pelo Rust ao criar/reposicionar a
// janela; invoke("set_overlay_position") já cuida de mover ao vivo se o
// overlay já estiver aberto.
export function OverlayPositionButton() {
  const [open, setOpen] = useState(false);
  const [corner, setCorner] = useState<Corner>("bottom-right");

  useEffect(() => {
    let cancelled = false;
    getStore()
      .then((store) => store.get<Corner>(OVERLAY_CORNER_KEY))
      .then((value) => {
        if (!cancelled && value) setCorner(value);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function choose(value: Corner) {
    setCorner(value);
    setOpen(false);
    await invoke("set_overlay_position", { corner: value });
  }

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center justify-center gap-2 rounded-lg border border-white/15 bg-white/5 py-3 text-sm font-semibold text-white transition-colors hover:bg-white/10"
      >
        <PictureInPicture className="size-5" />
        Posição do cronômetro na tela
      </button>

      {open && (
        <div className="absolute inset-x-0 bottom-full mb-2 rounded-lg border border-white/15 bg-[#04325A] p-3 shadow-lg">
          <p className="mb-2 text-center text-xs text-white/60">Onde o widget fica na tela</p>
          <div className="grid aspect-video grid-cols-2 grid-rows-2 gap-1.5 rounded-md border border-white/10 bg-white/5 p-1.5">
            {CORNERS.map((c) => (
              <button
                key={c.value}
                type="button"
                title={c.label}
                onClick={() => choose(c.value)}
                className={`rounded-md text-xs font-medium transition-colors ${
                  corner === c.value
                    ? "bg-[#0DB88F] text-white"
                    : "bg-white/5 text-white/50 hover:bg-white/15 hover:text-white"
                }`}
              >
                ●
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
