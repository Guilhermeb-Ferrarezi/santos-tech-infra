import { useCallback, useEffect, useState } from "react";
import { load, type Store } from "@tauri-apps/plugin-store";

const STORE_FILE = "config.json";
const TOKEN_KEY = "token";

let storePromise: Promise<Store> | null = null;
function getStore() {
  if (!storePromise) storePromise = load(STORE_FILE, { autoSave: true });
  return storePromise;
}

// Guarda o token da sessão em disco (config.json no diretório de dados do
// app) pra sobreviver a reinício do PC — o mesmo PC atende vários clientes
// ao longo do dia, então precisa trocar de sessão sem perder o pareamento
// se reiniciar no meio de um turno.
export function useStoredToken() {
  const [token, setTokenState] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    getStore()
      .then((store) => store.get<string>(TOKEN_KEY))
      .then((value) => {
        if (!cancelled) setTokenState(value ?? null);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const setToken = useCallback(async (value: string) => {
    const store = await getStore();
    await store.set(TOKEN_KEY, value);
    setTokenState(value);
  }, []);

  const clearToken = useCallback(async () => {
    const store = await getStore();
    await store.delete(TOKEN_KEY);
    await store.save();
    setTokenState(null);
  }, []);

  // Apaga o token do DISCO mas mantém o valor em memória. Serve pro fim de
  // sessão (status "ended"): o token de 64 hex fica em claro no config.json de
  // um PC que atende vários clientes por dia, e uma sessão já encerrada não
  // tem motivo nenhum pra sobreviver ao próximo turno — a partir daqui ele
  // morre junto com o processo. Mantendo em memória, a tela ainda consegue
  // mostrar "Sessão encerrada" em vez de saltar pro pareamento no mesmo frame.
  const purgeStoredToken = useCallback(async () => {
    const store = await getStore();
    await store.delete(TOKEN_KEY);
    await store.save();
  }, []);

  return { token, loading, setToken, clearToken, purgeStoredToken };
}
