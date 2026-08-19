import React, { useEffect, useState } from "react";
import ReactDOM from "react-dom/client";
import { load, type Store } from "@tauri-apps/plugin-store";
import { getCurrentWindow } from "@tauri-apps/api/window";
import { BellRinging, X } from "@phosphor-icons/react";
import "./index.css";

const STORE_FILE = "config.json";
const TOAST_MESSAGE_KEY = "toastMessage";
const POLL_INTERVAL_MS = 2_000;

let storePromise: Promise<Store> | null = null;
function getStore() {
  if (!storePromise) storePromise = load(STORE_FILE, { autoSave: true });
  return storePromise;
}

interface ToastMessage {
  id: string;
  text: string;
}

const appWindow = getCurrentWindow();

// Popup de aviso do admin, estilo notificação de antivírus — canto inferior
// direito, por cima de tudo, mesmo com a janela principal minimizada na
// bandeja (é uma janela própria, ver ensure_toast_window em lib.rs). Faz seu
// próprio poll no store (mesmo padrão do overlay.tsx) — useDeviceHeartbeat.ts,
// na janela principal, já dedupe por id antes de gravar aqui, então só
// precisamos lembrar localmente qual id já foi fechado nesta sessão do app.
function ToastApp() {
  const [message, setMessage] = useState<ToastMessage | null>(null);
  const [dismissedId, setDismissedId] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function poll() {
      const store = await getStore();
      const stored = await store.get<ToastMessage>(TOAST_MESSAGE_KEY);
      if (cancelled || !stored) return;
      setMessage((current) => (current?.id === stored.id ? current : stored));
    }
    poll();
    const id = setInterval(poll, POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  const visible = !!message && message.id !== dismissedId;

  useEffect(() => {
    if (visible) appWindow.show();
    else appWindow.hide();
  }, [visible]);

  if (!visible || !message) return null;

  return (
    <div className="flex h-screen w-screen items-start gap-2.5 rounded-xl border border-white/10 bg-[#0E2937] p-3.5 text-white shadow-2xl">
      <BellRinging className="mt-0.5 size-5 shrink-0 text-[#0DB88F]" />
      <div className="min-w-0 flex-1">
        <p className="text-xs font-semibold uppercase tracking-wide text-white/60">Aviso</p>
        <p className="mt-1 text-sm leading-snug break-words">{message.text}</p>
      </div>
      <button
        type="button"
        onClick={() => setDismissedId(message.id)}
        title="Fechar"
        className="shrink-0 rounded p-0.5 text-white/60 transition-colors hover:bg-white/10 hover:text-white"
      >
        <X className="size-4" />
      </button>
    </div>
  );
}

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <ToastApp />
  </React.StrictMode>,
);
