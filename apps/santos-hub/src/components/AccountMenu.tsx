import { useState } from "react";
import { SignOut, User, Warning } from "@phosphor-icons/react";
import { useAuth, type AuthUser } from "../lib/useAuth";
import { ApiError } from "../lib/api";

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
