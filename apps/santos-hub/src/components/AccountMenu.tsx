import { useState } from "react";
import { CaretDown, GoogleLogo, QrCode, SignOut, User, Warning } from "@phosphor-icons/react";
import { openUrl } from "@tauri-apps/plugin-opener";
import { useAuth, type AuthUser } from "../lib/useAuth";
import { ApiError } from "../lib/api";
import { QRScanModal } from "./QRScanModal";

// Página que gera o QR/código (usuário já logado no navegador é quem
// autoriza este app) — ver dashboard/web:/conectar-dispositivo.
const CONNECT_DEVICE_URL = "https://santos-tech.com/dashboard/conectar-dispositivo";

function Avatar({ user }: { user: AuthUser | null }) {
  if (user?.avatarUrl) {
    return <img src={user.avatarUrl} alt={user.name} className="size-7 rounded-full object-cover" />;
  }
  if (user) {
    const initials = user.name.trim().slice(0, 1).toUpperCase() || "?";
    return (
      <div className="flex size-7 items-center justify-center rounded-full bg-[#0DB88F] text-xs font-bold text-white">
        {initials}
      </div>
    );
  }
  return (
    <div className="flex size-7 items-center justify-center rounded-full bg-neutral-700 text-neutral-300">
      <User className="size-4" />
    </div>
  );
}

type MfaChallenge = { challenge: string; method: "totp" | "email"; methods: string[] };

function LoginForm({ onDone }: { onDone: () => void }) {
  const { login, verifyMfa, resendMfaEmail, loginWithCode } = useAuth();
  const [identifier, setIdentifier] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [mfa, setMfa] = useState<MfaChallenge | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [showOther, setShowOther] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [deviceCode, setDeviceCode] = useState("");

  async function submitCode(value: string) {
    setError("");
    setBusy(true);
    try {
      await loginWithCode(value.trim());
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Código inválido ou expirado.");
    } finally {
      setBusy(false);
    }
  }

  async function submitLogin(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      const result = await login(identifier.trim(), password);
      if (result.mfaRequired) {
        setMfa({ challenge: result.challenge, method: result.method, methods: result.methods });
      } else {
        onDone();
      }
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Falha ao entrar — confira suas credenciais.");
    } finally {
      setBusy(false);
    }
  }

  async function submitMfa(e: React.FormEvent) {
    e.preventDefault();
    if (!mfa) return;
    setError("");
    setBusy(true);
    try {
      await verifyMfa(mfa.challenge, code.trim());
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Código inválido.");
    } finally {
      setBusy(false);
    }
  }

  if (mfa) {
    return (
      <form onSubmit={submitMfa} className="space-y-2 p-3">
        <p className="text-xs text-neutral-400">
          {mfa.method === "email" ? "Código enviado pro seu email." : "Código do app autenticador."}
        </p>
        <input
          autoFocus
          value={code}
          onChange={(e) => setCode(e.target.value)}
          placeholder="Código"
          className="w-full rounded-lg border border-white/10 bg-white/5 px-3 py-2 text-sm text-white outline-none placeholder:text-white/30 focus:border-[#0DB88F]"
        />
        {error && (
          <p className="flex items-center gap-1 text-xs text-red-400">
            <Warning className="size-3.5 shrink-0" /> {error}
          </p>
        )}
        <button
          type="submit"
          disabled={busy || !code.trim()}
          className="w-full rounded-lg bg-[#0DB88F] py-2 text-sm font-semibold text-white transition-colors hover:bg-[#0DB88F]/90 disabled:cursor-not-allowed disabled:opacity-40"
        >
          Confirmar
        </button>
        {mfa.method === "email" && (
          <button
            type="button"
            onClick={() => resendMfaEmail(mfa.challenge)}
            className="w-full text-center text-xs text-neutral-400 hover:text-white"
          >
            Reenviar código
          </button>
        )}
      </form>
    );
  }

  return (
    <form onSubmit={submitLogin} className="space-y-2 p-3">
      <input
        autoFocus
        value={identifier}
        onChange={(e) => setIdentifier(e.target.value)}
        placeholder="Email ou usuário"
        className="w-full rounded-lg border border-white/10 bg-white/5 px-3 py-2 text-sm text-white outline-none placeholder:text-white/30 focus:border-[#0DB88F]"
      />
      <input
        type="password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        placeholder="Senha"
        className="w-full rounded-lg border border-white/10 bg-white/5 px-3 py-2 text-sm text-white outline-none placeholder:text-white/30 focus:border-[#0DB88F]"
      />
      {error && (
        <p className="flex items-center gap-1 text-xs text-red-400">
          <Warning className="size-3.5 shrink-0" /> {error}
        </p>
      )}
      <button
        type="submit"
        disabled={busy || !identifier.trim() || !password}
        className="w-full rounded-lg bg-[#0DB88F] py-2 text-sm font-semibold text-white transition-colors hover:bg-[#0DB88F]/90 disabled:cursor-not-allowed disabled:opacity-40"
      >
        Entrar
      </button>

      <button
        type="button"
        onClick={() => setShowOther((v) => !v)}
        className="flex w-full items-center justify-center gap-1 pt-1 text-xs text-neutral-400 hover:text-white"
      >
        Outras opções
        <CaretDown className={`size-3 transition-transform ${showOther ? "rotate-180" : ""}`} />
      </button>

      {showOther && (
        <div className="space-y-2 border-t border-white/10 pt-2">
          <button
            type="button"
            onClick={() => setScanning(true)}
            className="flex w-full items-center justify-center gap-2 rounded-lg border border-white/10 py-2 text-xs font-medium text-white transition-colors hover:bg-white/5"
          >
            <QrCode className="size-4" /> Escanear QR code
          </button>

          <div className="flex items-center gap-2">
            <input
              value={deviceCode}
              onChange={(e) => setDeviceCode(e.target.value)}
              placeholder="Código de 6 dígitos"
              maxLength={6}
              className="w-full rounded-lg border border-white/10 bg-white/5 px-3 py-1.5 text-sm text-white outline-none placeholder:text-white/30 focus:border-[#0DB88F]"
            />
            <button
              type="button"
              disabled={busy || deviceCode.trim().length < 6}
              onClick={() => submitCode(deviceCode)}
              className="shrink-0 rounded-lg bg-white/10 px-3 py-1.5 text-xs font-semibold text-white transition-colors hover:bg-white/20 disabled:cursor-not-allowed disabled:opacity-40"
            >
              Entrar
            </button>
          </div>

          <button
            type="button"
            onClick={() => openUrl(CONNECT_DEVICE_URL)}
            className="flex w-full items-center justify-center gap-2 rounded-lg border border-white/10 py-2 text-xs font-medium text-white transition-colors hover:bg-white/5"
          >
            <GoogleLogo className="size-4" /> Entrar com Google (abre o navegador)
          </button>
          <p className="text-center text-[11px] text-neutral-500">
            Gere o QR/código em santos-tech.com/dashboard/conectar-dispositivo
          </p>
        </div>
      )}

      {scanning && (
        <QRScanModal
          onClose={() => setScanning(false)}
          onResult={(text) => {
            setScanning(false);
            submitCode(text);
          }}
        />
      )}
    </form>
  );
}

export function AccountMenu() {
  const { user, loading, logout } = useAuth();
  const [open, setOpen] = useState(false);

  if (loading) {
    return <div className="size-7 shrink-0 animate-pulse rounded-full bg-neutral-700" />;
  }

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        title={user ? user.name : "Entrar com sua conta Santos Tech"}
        className="block rounded-full ring-1 ring-white/10 transition-shadow hover:ring-white/30"
      >
        <Avatar user={user} />
      </button>

      {open && (
        <>
          <button
            type="button"
            aria-label="Fechar"
            onClick={() => setOpen(false)}
            className="fixed inset-0 z-40 cursor-default"
          />
          <div className="absolute right-0 top-9 z-50 w-56 overflow-hidden rounded-xl border border-white/10 bg-neutral-800 shadow-xl">
            {user ? (
              <div>
                <div className="flex items-center gap-2 border-b border-white/10 p-3">
                  <Avatar user={user} />
                  <div className="min-w-0">
                    <p className="truncate text-sm font-semibold text-white">{user.name}</p>
                    <p className="truncate text-xs text-neutral-400">{user.email}</p>
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => {
                    logout();
                    setOpen(false);
                  }}
                  className="flex w-full items-center gap-2 px-3 py-2.5 text-left text-sm text-neutral-300 transition-colors hover:bg-white/5 hover:text-white"
                >
                  <SignOut className="size-4" /> Sair
                </button>
              </div>
            ) : (
              <LoginForm onDone={() => setOpen(false)} />
            )}
          </div>
        </>
      )}
    </div>
  );
}
