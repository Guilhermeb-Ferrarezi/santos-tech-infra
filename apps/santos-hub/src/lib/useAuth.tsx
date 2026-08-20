import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { apiFetch, apiJSON, ApiError } from "./api";

// Sessão SÓ em memória (nada de disco/tauri-plugin-store): fechar o app
// destrói o login. De propósito — o Hub roda em PCs compartilhados da
// empresa, então logar com Google aqui é "uso único" (baixa o que precisa,
// fecha), nunca uma sessão que sobrevive pro próximo que abrir o app.
let memTokens: { accessToken: string; refreshToken: string } | null = null;

export interface AuthUser {
  id: number;
  name: string;
  email: string;
  avatarUrl: string | null;
}

interface Tokens {
  accessToken: string;
  refreshToken: string;
}

interface LoginOk {
  mfaRequired: false;
}
interface LoginMfa {
  mfaRequired: true;
  challenge: string;
  method: "totp" | "email";
  methods: string[];
}
type LoginResult = LoginOk | LoginMfa;

// Login/MFA/refresh nativos sempre devolvem o par de tokens direto no corpo
// (ver Convenções do llms.txt — cliente sem header Origin) — nunca cookie.
function persistSession(body: { user: AuthUser; accessToken?: string; refreshToken?: string }) {
  if (!body.accessToken || !body.refreshToken) {
    throw new Error("resposta de login sem accessToken/refreshToken — backend tratou como cliente browser?");
  }
  memTokens = { accessToken: body.accessToken, refreshToken: body.refreshToken };
}

// Central de identificação (avatar/nome no canto do app) — NÃO é gate de
// acesso: o catálogo (/public/downloads) continua público sem login nenhum,
// isso aqui é só personalização opcional.
//
// Context, não hook solto: AccountMenu, LoginForm e QRLoginPanel precisam
// enxergar o MESMO estado — com um hook comum cada `useAuth()` teria seu
// próprio `user` isolado, e o login por QR (que atualiza a instância de
// dentro do QRLoginPanel) nunca apareceria no avatar do AccountMenu.
type AuthContextValue = ReturnType<typeof useProvideAuth>;
const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const value = useProvideAuth();
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth() precisa estar dentro de <AuthProvider>");
  return ctx;
}

function useProvideAuth() {
  const [user, setUser] = useState<AuthUser | null>(null);
  const [loading, setLoading] = useState(true);
  const refreshing = useRef<Promise<Tokens | null> | null>(null);

  const clearSession = useCallback(async () => {
    memTokens = null;
    setUser(null);
  }, []);

  const doRefresh = useCallback(async (): Promise<Tokens | null> => {
    if (refreshing.current) return refreshing.current;
    const task = (async () => {
      const refreshToken = memTokens?.refreshToken;
      if (!refreshToken) return null;
      try {
        const res = await apiFetch("/auth/refresh", {
          method: "POST",
          headers: { Authorization: `Bearer ${refreshToken}` },
        });
        if (!res.ok) return null;
        const body: Tokens = await res.json();
        memTokens = { accessToken: body.accessToken, refreshToken: body.refreshToken };
        return body;
      } catch {
        return null;
      }
    })();
    refreshing.current = task;
    try {
      return await task;
    } finally {
      refreshing.current = null;
    }
  }, []);

  // fetchMe tenta com o access token guardado; se der 401, tenta renovar uma
  // vez antes de desistir (mesmo padrão de qualquer client com refresh token).
  // GET /auth/me devolve {"user": {...}} — envelopado, igual /auth/login e
  // /auth/mfa/verify (ver handleMe em handlers_auth.go) — nunca o objeto do
  // usuário direto no corpo.
  const fetchMe = useCallback(async (): Promise<AuthUser | null> => {
    let accessToken = memTokens?.accessToken;
    if (!accessToken) return null;
    try {
      const body = await apiJSON<{ user: AuthUser }>("/auth/me", { headers: { Authorization: `Bearer ${accessToken}` } });
      return body.user;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        const refreshed = await doRefresh();
        if (!refreshed) return null;
        accessToken = refreshed.accessToken;
        try {
          const body = await apiJSON<{ user: AuthUser }>("/auth/me", { headers: { Authorization: `Bearer ${accessToken}` } });
          return body.user;
        } catch {
          return null;
        }
      }
      return null;
    }
  }, [doRefresh]);

  useEffect(() => {
    let cancelled = false;
    fetchMe()
      .then((u) => {
        if (!cancelled) setUser(u);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [fetchMe]);

  const login = useCallback(async (identifier: string, password: string): Promise<LoginResult> => {
    const body = await apiJSON<{ user?: AuthUser; accessToken?: string; refreshToken?: string; mfaRequired?: boolean; challenge?: string; method?: "totp" | "email"; methods?: string[] }>(
      "/auth/login",
      { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ identifier, password }) },
    );
    if (body.mfaRequired) {
      return { mfaRequired: true, challenge: body.challenge!, method: body.method!, methods: body.methods ?? [] };
    }
    await persistSession(body as { user: AuthUser; accessToken: string; refreshToken: string });
    const me = await fetchMe();
    setUser(me);
    return { mfaRequired: false };
  }, [fetchMe]);

  const verifyMfa = useCallback(async (challenge: string, code: string) => {
    const body = await apiJSON<{ user: AuthUser; accessToken?: string; refreshToken?: string }>("/auth/mfa/verify", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ challenge, code }),
    });
    await persistSession(body);
    const me = await fetchMe();
    setUser(me);
  }, [fetchMe]);

  // Login por QR code (estilo WhatsApp Web): o Hub GERA e MOSTRA o QR/código
  // (createQRLogin) e fica com poll (pollQRLogin) até alguém já logado
  // confirmar em dashboard/web:/conectar-dispositivo — ver handlers_qr_login.go.
  const createQRLogin = useCallback(async () => {
    return apiJSON<{ token: string; code: string; expiresAt: string }>("/public/qr-login/create", {
      method: "POST",
    });
  }, []);

  // Devolve true quando a sessão chegou (já persistida e setUser já chamado).
  const pollQRLogin = useCallback(async (token: string): Promise<boolean> => {
    const body = await apiJSON<{ ready: boolean; user?: AuthUser; accessToken?: string; refreshToken?: string }>(
      `/public/qr-login/poll?token=${encodeURIComponent(token)}`,
    );
    if (!body.ready) return false;
    await persistSession(body as { user: AuthUser; accessToken: string; refreshToken: string });
    const me = await fetchMe();
    setUser(me);
    return true;
  }, [fetchMe]);

  const resendMfaEmail = useCallback(async (challenge: string) => {
    await apiFetch("/auth/mfa/email", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ challenge }),
    });
  }, []);

  const logout = useCallback(async () => {
    const accessToken = memTokens?.accessToken;
    if (accessToken) {
      // best-effort — mesmo se falhar, a sessão local já é limpa embaixo
      apiFetch("/auth/logout", { method: "POST", headers: { Authorization: `Bearer ${accessToken}` } }).catch(() => {});
    }
    await clearSession();
  }, [clearSession]);

  return { user, loading, login, verifyMfa, resendMfaEmail, createQRLogin, pollQRLogin, logout };
}
