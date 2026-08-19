import { useCallback, useEffect, useRef, useState } from "react";
import { load, type Store } from "@tauri-apps/plugin-store";
import { apiFetch, apiJSON, ApiError } from "./api";

const STORE_FILE = "auth.json";
const ACCESS_KEY = "accessToken";
const REFRESH_KEY = "refreshToken";

let storePromise: Promise<Store> | null = null;
function getStore() {
  if (!storePromise) storePromise = load(STORE_FILE, { autoSave: true });
  return storePromise;
}

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
async function persistSession(body: { user: AuthUser; accessToken?: string; refreshToken?: string }) {
  if (!body.accessToken || !body.refreshToken) {
    throw new Error("resposta de login sem accessToken/refreshToken — backend tratou como cliente browser?");
  }
  const store = await getStore();
  await store.set(ACCESS_KEY, body.accessToken);
  await store.set(REFRESH_KEY, body.refreshToken);
}

// Central de identificação (avatar/nome no canto do app) — NÃO é gate de
// acesso: o catálogo (/public/downloads) continua público sem login nenhum,
// isso aqui é só personalização opcional.
export function useAuth() {
  const [user, setUser] = useState<AuthUser | null>(null);
  const [loading, setLoading] = useState(true);
  const refreshing = useRef<Promise<Tokens | null> | null>(null);

  const clearSession = useCallback(async () => {
    const store = await getStore();
    await store.delete(ACCESS_KEY);
    await store.delete(REFRESH_KEY);
    setUser(null);
  }, []);

  const doRefresh = useCallback(async (): Promise<Tokens | null> => {
    if (refreshing.current) return refreshing.current;
    const task = (async () => {
      const store = await getStore();
      const refreshToken = await store.get<string>(REFRESH_KEY);
      if (!refreshToken) return null;
      try {
        const res = await apiFetch("/auth/refresh", {
          method: "POST",
          headers: { Authorization: `Bearer ${refreshToken}` },
        });
        if (!res.ok) return null;
        const body: Tokens = await res.json();
        await store.set(ACCESS_KEY, body.accessToken);
        await store.set(REFRESH_KEY, body.refreshToken);
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
  const fetchMe = useCallback(async (): Promise<AuthUser | null> => {
    const store = await getStore();
    let accessToken = await store.get<string>(ACCESS_KEY);
    if (!accessToken) return null;
    try {
      return await apiJSON<AuthUser>("/auth/me", { headers: { Authorization: `Bearer ${accessToken}` } });
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        const refreshed = await doRefresh();
        if (!refreshed) return null;
        accessToken = refreshed.accessToken;
        try {
          return await apiJSON<AuthUser>("/auth/me", { headers: { Authorization: `Bearer ${accessToken}` } });
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

  const resendMfaEmail = useCallback(async (challenge: string) => {
    await apiFetch("/auth/mfa/email", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ challenge }),
    });
  }, []);

  const logout = useCallback(async () => {
    const store = await getStore();
    const accessToken = await store.get<string>(ACCESS_KEY);
    if (accessToken) {
      // best-effort — mesmo se falhar, a sessão local já é limpa embaixo
      apiFetch("/auth/logout", { method: "POST", headers: { Authorization: `Bearer ${accessToken}` } }).catch(() => {});
    }
    await clearSession();
  }, [clearSession]);

  return { user, loading, login, verifyMfa, resendMfaEmail, logout };
}
