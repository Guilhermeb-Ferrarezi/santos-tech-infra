import { useEffect, useState } from "react";
import { Clock, ArrowCounterClockwise } from "@phosphor-icons/react";
import { QRCodeSVG } from "qrcode.react";
import { useStoredToken } from "./lib/useStoredToken";
import { useTickingSeconds } from "./lib/useTickingSeconds";
import { useLowBalanceNotifier } from "./lib/useLowBalanceNotifier";
import { useDeviceHeartbeat } from "./lib/useDeviceHeartbeat";
import { Titlebar } from "./components/Titlebar";
import { OverlayPositionButton } from "./components/OverlayPositionButton";

const API_ORIGIN = "https://api.santos-tech.com";
const DASHBOARD_ORIGIN = "https://santos-tech.com/dashboard";

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

// Aceita tanto o link completo (copiado do painel admin) quanto o token cru.
function extractToken(input: string): string | null {
  const trimmed = input.trim();
  const fromUrl = trimmed.match(/\/sessao\/([0-9a-f]{64})\/?$/i)?.[1];
  const raw = fromUrl ?? trimmed;
  return /^[0-9a-f]{64}$/i.test(raw) ? raw.toLowerCase() : null;
}

const statusText: Record<PublicHourSession["status"], string> = {
  active: "Jogando",
  paused: "Pausado",
  ended: "Sessão encerrada",
};

function PairScreen({ deviceId, onConfirm }: { deviceId: string | null; onConfirm: (token: string) => void }) {
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  const [pairing, setPairing] = useState(false);

  // Detecta pelo formato do que foi digitado: 6 dígitos = código curto (troca
  // por um token novo no backend, ver POST /public/hour-sessions/pair-by-code);
  // qualquer outra coisa tenta como link/token cru (sem chamada de rede).
  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = value.trim();
    if (/^\d{6}$/.test(trimmed)) {
      setPairing(true);
      setError("");
      try {
        const res = await fetch(`${API_ORIGIN}/public/hour-sessions/pair-by-code`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ code: trimmed }),
        });
        if (!res.ok) {
          setError("Código inválido ou expirado — peça um código novo pro colaborador.");
          return;
        }
        const data: { token: string } = await res.json();
        onConfirm(data.token);
      } catch {
        setError("Falha ao conectar — confira a internet e tente de novo.");
      } finally {
        setPairing(false);
      }
      return;
    }
    const token = extractToken(trimmed);
    if (!token) {
      setError("Link, token ou código inválido — cole o link (ou digite o código de 6 dígitos) que o colaborador te passou.");
      return;
    }
    setError("");
    onConfirm(token);
  }

  return (
    <div className="grid flex-1 place-items-center overflow-y-auto bg-[#04325A] px-6 py-6 text-white">
      <div className="w-full max-w-sm space-y-5">
        <div className="text-center">
          <p className="text-lg font-bold">Santos Tech</p>
          <p className="text-sm text-white/70">Escaneie o QR com o celular do colaborador</p>
        </div>

        <div className="flex flex-col items-center gap-2 rounded-2xl bg-white p-4 shadow-lg shadow-black/30">
          {deviceId ? (
            <QRCodeSVG value={`${DASHBOARD_ORIGIN}/admin/horas/parear/${deviceId}`} size={168} />
          ) : (
            <div className="grid size-[168px] place-items-center text-xs text-[#04325A]/60">Conectando...</div>
          )}
        </div>

        <div className="flex items-center gap-3 text-xs text-white/40">
          <div className="h-px flex-1 bg-white/15" />
          ou
          <div className="h-px flex-1 bg-white/15" />
        </div>

        <form onSubmit={onSubmit} className="space-y-4">
          <p className="text-center text-sm text-white/70">Cole o link ou digite o código de 6 dígitos da sessão</p>
          <input
            autoFocus
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="Link ou código de 6 dígitos..."
            className="w-full rounded-lg border border-white/20 bg-white/5 px-3 py-2 text-center text-sm text-white outline-none placeholder:text-white/40 focus:border-[#0DB88F]"
          />
          {error && <p className="text-xs text-red-300">{error}</p>}
          <button
            type="submit"
            disabled={pairing}
            className="w-full rounded-lg bg-[#0DB88F] py-2 text-sm font-semibold text-white hover:bg-[#0DB88F]/90 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {pairing ? "Conectando..." : "Confirmar"}
          </button>
        </form>
      </div>
    </div>
  );
}

function TimerScreen({ token, onChangeSession }: { token: string; onChangeSession: () => void }) {
  const [data, setData] = useState<PublicHourSession | null>(null);
  const [error, setError] = useState(false);

  useEffect(() => {
    let cancelled = false;
    async function poll() {
      try {
        const res = await fetch(`${API_ORIGIN}/public/hour-sessions/${token}`);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const json: PublicHourSession = await res.json();
        if (!cancelled) {
          setData(json);
          setError(false);
        }
      } catch {
        if (!cancelled) setError(true);
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
  useLowBalanceNotifier(data ? remainingMinutes : null, data?.status === "active");

  return (
    <div className="flex flex-1 flex-col justify-between bg-[#04325A] px-4 py-6 text-white">
      <div className="flex items-center justify-between">
        <p className="text-sm font-medium text-white/70">{data ? data.clientName : "Carregando..."}</p>
        <button
          type="button"
          onClick={onChangeSession}
          className="flex items-center gap-1 rounded-full px-2 py-1 text-xs text-white/50 transition-colors hover:bg-white/10 hover:text-white"
          title="Trocar sessão"
        >
          <ArrowCounterClockwise className="size-3.5" />
          Trocar sessão
        </button>
      </div>

      <div className="rounded-2xl bg-white/5 p-6 text-center shadow-sm ring-1 ring-white/10">
        {error && !data && <p className="text-sm text-red-300">Falha ao conectar — tentando de novo...</p>}
        {!data && !error && <p className="py-10 text-white/70">Carregando sessão...</p>}
        {data && (
          <>
            <p className="text-xs font-semibold uppercase tracking-[0.2em] text-[#0DB88F]">
              {statusText[data.status]}
            </p>
            <p className="mt-2 flex items-center justify-center gap-2 font-mono text-6xl font-bold tabular-nums">
              <Clock className="size-9 text-white/50" />
              {formatDuration(displaySeconds)}
            </p>
            <p className="mt-4 text-sm text-white/70">
              Saldo restante:{" "}
              <span className={remainingMinutes < 0 ? "font-semibold text-red-300" : "font-semibold"}>
                {remainingMinutes} min
              </span>
            </p>
          </>
        )}
      </div>

      <OverlayPositionButton />
    </div>
  );
}

export default function App() {
  const { token, loading, setToken, clearToken } = useStoredToken();
  const { deviceName, deviceId } = useDeviceHeartbeat(token, clearToken, setToken);

  return (
    <div className="flex h-screen flex-col overflow-hidden">
      <Titlebar deviceName={deviceName} />
      {loading && (
        <div className="grid flex-1 place-items-center bg-[#04325A] text-white/60">Carregando...</div>
      )}
      {!loading && !token && <PairScreen deviceId={deviceId} onConfirm={setToken} />}
      {!loading && token && <TimerScreen token={token} onChangeSession={clearToken} />}
    </div>
  );
}
