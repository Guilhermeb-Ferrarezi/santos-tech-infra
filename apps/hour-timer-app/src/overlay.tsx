import React, { useEffect, useState } from "react";
import ReactDOM from "react-dom/client";
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

// Widget transparente, só texto — nasce a partir do menu da bandeja (ver
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

  // O backend só devolve o saldo em minutos inteiros (remainingMinutes =
  // balance - floor(elapsedSeconds/60)). Como floor(elapsedSeconds/60) é
  // exatamente o que falta pra achar o segundo exato, dá pra reconstruir a
  // contagem regressiva sem precisão extra do servidor: minutos*60 menos o
  // resto de segundos dentro do minuto atual.
  const remainingSeconds = Math.max(0, (data?.remainingMinutes ?? 0) * 60 - (displaySeconds % 60));
  const low = remainingSeconds < 10 * 60;

  return (
    <div className="flex h-screen w-screen select-none flex-col items-center justify-center gap-0.5 bg-transparent text-white [text-shadow:0_1px_6px_rgba(0,0,0,0.9)]">
      <p className="text-xs font-medium text-white/80">Tempo restante:</p>
      <p className={`font-mono text-4xl font-bold tabular-nums ${low ? "text-red-400" : "text-white"}`}>
        {data ? formatDuration(remainingSeconds) : "--:--"}
      </p>
    </div>
  );
}

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <OverlayApp />
  </React.StrictMode>,
);
