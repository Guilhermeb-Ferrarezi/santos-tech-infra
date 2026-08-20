import { useState } from "react";
import { CaretDown, SignOut, User, Warning } from "@phosphor-icons/react";
import { openUrl } from "@tauri-apps/plugin-opener";
import { useAuth, type AuthUser } from "../lib/useAuth";
import { ApiError } from "../lib/api";
import { QRLoginPanel } from "./QRLoginPanel";

// Página que gera o QR/código (usuário já logado no navegador é quem
// autoriza este app) — ver dashboard/web:/conectar-dispositivo.
const CONNECT_DEVICE_URL = "https://santos-tech.com/dashboard/conectar-dispositivo";

// Logo oficial do Google (4 cores) — o phosphor-icons só tem um "G" monocromático,
// e o botão "Entrar com Google" fica sem graça nenhuma sem a marca reconhecível.
function GoogleG({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 48 48" className={className}>
      <path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z" />
      <path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z" />
      <path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z" />
      <path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z" />
    </svg>
  );
}

// Linha de opção do "Outras opções": badge colorido + label (+ subtítulo opcional).
function OptionRow({
  icon,
  iconBg,
  label,
  sublabel,
  onClick,
}: {
  icon: React.ReactNode;
  iconBg: string;
  label: string;
  sublabel?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex w-full items-center gap-2.5 rounded-lg border border-white/10 px-2.5 py-2 text-left transition-colors hover:bg-white/5"
    >
      <span className={`flex size-8 shrink-0 items-center justify-center rounded-lg ${iconBg}`}>{icon}</span>
      <span className="min-w-0 flex-1">
        <span className="block text-xs font-medium text-white">{label}</span>
        {sublabel && <span className="block truncate text-[10px] text-neutral-400">{sublabel}</span>}
      </span>
    </button>
  );
}

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
  const { login, verifyMfa, resendMfaEmail } = useAuth();
  const [identifier, setIdentifier] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [mfa, setMfa] = useState<MfaChallenge | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [showOther, setShowOther] = useState(false);

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

      <div className="relative flex items-center pt-1">
        <div className="h-px flex-1 bg-white/10" />
        <button
          type="button"
          onClick={() => setShowOther((v) => !v)}
          className="flex shrink-0 items-center gap-1 px-2 text-[11px] font-medium uppercase tracking-wide text-neutral-400 hover:text-white"
        >
          Outras opções
          <CaretDown className={`size-3 transition-transform ${showOther ? "rotate-180" : ""}`} />
        </button>
        <div className="h-px flex-1 bg-white/10" />
      </div>

      {showOther && (
        <div className="space-y-2">
          <QRLoginPanel onDone={onDone} />

          <OptionRow
            icon={<GoogleG className="size-4" />}
            iconBg="bg-white"
            label="Entrar com Google"
            sublabel="Abre no navegador"
            onClick={() => openUrl(CONNECT_DEVICE_URL)}
          />

          <p className="text-center text-[10px] leading-relaxed text-neutral-500">
            Escaneie com a câmera do celular, ou digite o código em{" "}
            <span className="text-neutral-400">santos-tech.com/dashboard/conectar-dispositivo</span>
          </p>
        </div>
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
          <div className="absolute right-0 top-9 z-50 w-64 overflow-hidden rounded-xl border border-white/10 bg-neutral-800 shadow-xl">
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
