import { useEffect, useMemo, useState } from "react";
import { openUrl } from "@tauri-apps/plugin-opener";
import {
  ArrowClockwise,
  DownloadSimple,
  LinkSimple,
  MagnifyingGlass,
  Package,
  PushPin,
  WarningCircle,
} from "@phosphor-icons/react";
import { Titlebar } from "./components/Titlebar";
import { AccountMenu } from "./components/AccountMenu";
import { apiJSON, hubHeaders } from "./lib/api";

interface DownloadItem {
  id: number;
  name: string;
  description: string;
  category: string;
  version: string;
  kind: "file" | "link";
  url: string;
  sizeBytes?: number;
  pinned: boolean;
  updatedAt: string;
  imageUrl?: string;
}

// O catálogo (/public/downloads) é dado remoto: `url` vem do banco e o app
// mandava direto pro openUrl. Como o escopo do opener era http://*/https://*
// (opener:default), um item adulterado abria qualquer coisa em TODOS os PCs da
// empresa. O escopo real agora vive em src-tauri/capabilities/default.json —
// esta lista precisa espelhá-lo, e só existe pra dar mensagem de erro decente:
// sem ela o openUrl seria rejeitado pelo Tauri sem nenhum sinal na tela.
const ALLOWED_DOWNLOAD_ORIGINS = ["https://cdn.santos-tech.com", "https://santos-tech.com"];

function isAllowedDownloadUrl(raw: string) {
  try {
    return ALLOWED_DOWNLOAD_ORIGINS.includes(new URL(raw).origin);
  } catch {
    return false;
  }
}

function formatSize(bytes?: number) {
  if (!bytes) return null;
  const units = ["B", "KB", "MB", "GB"];
  let n = bytes;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

function DownloadCard({ item }: { item: DownloadItem }) {
  const [opening, setOpening] = useState(false);
  const [blocked, setBlocked] = useState(false);

  async function handleOpen() {
    if (!isAllowedDownloadUrl(item.url)) {
      setBlocked(true);
      return;
    }
    setOpening(true);
    setBlocked(false);
    try {
      await openUrl(item.url);
    } catch {
      setBlocked(true);
    } finally {
      setOpening(false);
    }
  }

  return (
    <div className="flex flex-col overflow-hidden rounded-xl bg-white/5 ring-1 ring-white/10">
      <div className="relative flex aspect-[3/4] items-center justify-center bg-white/5">
        {item.imageUrl ? (
          <img src={item.imageUrl} alt="" className="size-full object-cover" />
        ) : (
          <Package className="size-8 text-white/20" />
        )}
        {item.pinned && (
          <PushPin className="absolute right-2 top-2 size-3.5 text-[#0DB88F] drop-shadow" weight="fill" />
        )}
      </div>
      <div className="flex flex-1 flex-col gap-1 p-3">
        <div className="flex items-center gap-1.5">
          <p className="min-w-0 flex-1 truncate text-sm font-semibold text-white">{item.name}</p>
          {item.version && (
            <span className="shrink-0 rounded-full bg-white/10 px-2 py-0.5 text-[10px] font-medium text-white/70">
              v{item.version}
            </span>
          )}
        </div>
        {item.description && <p className="line-clamp-2 text-xs text-white/60">{item.description}</p>}
        <div className="flex items-center gap-2 text-[10px] uppercase tracking-wide text-white/40">
          {item.category && <span>{item.category}</span>}
          {item.sizeBytes ? <span>· {formatSize(item.sizeBytes)}</span> : null}
        </div>
        <button
          type="button"
          onClick={handleOpen}
          disabled={opening}
          title={item.kind === "file" ? "Baixar" : "Abrir link"}
          className="mt-2 flex items-center justify-center gap-1.5 rounded-lg bg-[#0DB88F] py-2 text-xs font-semibold text-white transition-colors hover:bg-[#0DB88F]/90 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {item.kind === "file" ? <DownloadSimple className="size-4" /> : <LinkSimple className="size-4" />}
          {item.kind === "file" ? "Baixar" : "Abrir"}
        </button>
        {blocked && (
          <p className="mt-1 flex items-start gap-1 text-[10px] leading-tight text-red-300">
            <WarningCircle className="mt-px size-3 shrink-0" />
            Link fora dos domínios permitidos — bloqueado por segurança.
          </p>
        )}
      </div>
    </div>
  );
}

export default function App() {
  const [items, setItems] = useState<DownloadItem[] | null>(null);
  const [error, setError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState("");

  async function load() {
    setLoading(true);
    try {
      const json = await apiJSON<{ items: DownloadItem[] }>("/public/downloads", {
        headers: hubHeaders(),
      });
      setItems(json.items);
      setError(false);
    } catch {
      setError(true);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  const filtered = useMemo(() => {
    if (!items) return [];
    const q = query.trim().toLowerCase();
    if (!q) return items;
    return items.filter(
      (i) => i.name.toLowerCase().includes(q) || i.category.toLowerCase().includes(q) || i.description.toLowerCase().includes(q),
    );
  }, [items, query]);

  const grouped = useMemo(() => {
    const map = new Map<string, DownloadItem[]>();
    for (const item of filtered) {
      const key = item.category || "Outros";
      if (!map.has(key)) map.set(key, []);
      map.get(key)!.push(item);
    }
    return [...map.entries()];
  }, [filtered]);

  return (
    <div className="flex h-screen flex-col overflow-hidden bg-neutral-950">
      <Titlebar />

      <div className="flex items-center gap-2 border-b border-white/10 px-4 py-3">
        <div className="relative flex-1">
          <MagnifyingGlass className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-white/40" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Buscar..."
            className="w-full rounded-lg border border-white/10 bg-white/5 py-2 pl-8 pr-3 text-sm text-white outline-none placeholder:text-white/40 focus:border-[#0DB88F]"
          />
        </div>
        <button
          type="button"
          onClick={load}
          title="Atualizar"
          className="flex size-8 shrink-0 items-center justify-center rounded-lg text-white/60 transition-colors hover:bg-white/10 hover:text-white"
        >
          <ArrowClockwise className={`size-4 ${loading ? "animate-spin" : ""}`} />
        </button>
        <AccountMenu />
      </div>

      <div className="flex-1 overflow-y-auto p-4">
        {loading && !items && <p className="py-10 text-center text-sm text-white/50">Carregando catálogo...</p>}

        {error && !items && (
          <div className="flex flex-col items-center gap-2 py-10 text-center text-white/60">
            <WarningCircle className="size-6 text-red-300" />
            <p className="text-sm">Falha ao conectar — tente atualizar.</p>
          </div>
        )}

        {items && items.length === 0 && (
          <p className="py-10 text-center text-sm text-white/50">Catálogo vazio por enquanto.</p>
        )}

        {items && items.length > 0 && filtered.length === 0 && (
          <p className="py-10 text-center text-sm text-white/50">Nada encontrado pra "{query}".</p>
        )}

        <div className="space-y-6">
          {grouped.map(([category, categoryItems]) => (
            <div key={category}>
              <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-white/40">{category}</p>
              <div className="grid grid-cols-2 gap-3">
                {categoryItems.map((item) => (
                  <DownloadCard key={item.id} item={item} />
                ))}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
