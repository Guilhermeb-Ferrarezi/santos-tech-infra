import React, { useEffect, useState } from "react";
import ReactDOM from "react-dom/client";
import { Clock } from "@phosphor-icons/react";
import { useStoredToken } from "./lib/useStoredToken";
import { useTickingSeconds } from "./lib/useTickingSeconds";
import "./index.css";

const API_ORIGIN = "https://api.santos-tech.com";

interface PublicHourSession {
  clientName: string;
  status: "active" | "paused" | "ended";
  elapsedSeconds: number;
  remainingMinutes: number;
  pauseRequested: boolean;
}

function formatDuration(totalSeconds: number) {
  const s = Math.max(0, Math.floor(totalSeconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(sec)}` : `${pad(m)}:${pad(sec)}`;
}

// Widget compacto, só o essencial — nasce a partir do menu da bandeja (ver
// toggle_overlay em src-tauri/src/lib.rs). Faz seu próprio poll, independente
// da janela principal — sem IPC entre janelas, só o token salvo em comum.
function OverlayApp() {
  const { token } = useStoredToken();
  const [data, setData] = useState<PublicHourSession | null>(null);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    async function poll() {
      try {
        const res = await fetch(`${API_ORIGIN}/public/hour-sessions/${token}`);
        if (!res.ok) return;
        const json: PublicHourSession = await res.json();
        if (!cancelled) setData(json);
      } catch {
        // silencioso — o próximo poll tenta de novo
      }
    }
    poll();
    const id = setInterval(poll, 5_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [token]);

  const displaySeconds = useTickingSeconds(data?.elapsedSeconds ?? 0, data?.status === "active");
  const tickedMinutes = Math.floor(displaySeconds / 60) - Math.floor((data?.elapsedSeconds ?? 0) / 60);
  const remainingMinutes = (data?.remainingMinutes ?? 0) - tickedMinutes;
  const low = remainingMinutes < 10;

  return (
    <div className="flex h-screen w-screen items-center gap-3 rounded-xl border border-white/10 bg-[#04325A] px-4 text-white">
      <Clock className={low ? "size-6 text-red-300" : "size-6 text-[#0DB88F]"} />
      <div className="min-w-0 flex-1">
        <p className="truncate text-[11px] text-white/60">{data?.clientName ?? (token ? "Carregando..." : "Sem sessão")}</p>
        <p className="font-mono text-xl font-bold tabular-nums leading-tight">{formatDuration(displaySeconds)}</p>
      </div>
      <p className={`text-xs font-semibold ${low ? "text-red-300" : "text-white/70"}`}>{remainingMinutes} min</p>
    </div>
  );
}

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <OverlayApp />
  </React.StrictMode>,
);
