import { useEffect, useRef } from "react";
import { invoke } from "@tauri-apps/api/core";

// Empurra status+tooltip pro ícone da bandeja (ver update_tray_status em
// lib.rs) sempre que algum dos dois mudar — dedupe simples pra não invocar o
// comando Rust a cada re-render/poll quando nada realmente mudou.
export function useTraySync(status: string, tooltip: string) {
  const last = useRef<string | null>(null);

  useEffect(() => {
    const key = `${status}|${tooltip}`;
    if (last.current === key) return;
    last.current = key;
    invoke("update_tray_status", { status, tooltip }).catch(() => {});
  }, [status, tooltip]);
}
