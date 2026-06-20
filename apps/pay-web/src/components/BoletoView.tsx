import { useEffect, useState } from "react";
import { Check, Copy, FileText, Loader2, XCircle } from "lucide-react";
import { api, type PayData } from "../lib/api";
import { Button } from "./ui/button";
import { BoletoBarcode } from "./BoletoBarcode";
import type { PixStatus } from "./PixView";

// BoletoView — etapa "Pagamento por boleto" (coluna esquerda do checkout). Mostra a
// linha digitável (copiar), o botão "Baixar boleto (PDF)" e confirma o pagamento ao vivo
// via SSE (a compensação do boleto pode levar até alguns dias úteis). Espelha o PixView;
// reusa o mesmo PixStatus para o botão do resumo (coluna direita).
export function BoletoView({
  token,
  onStatusChange,
}: {
  token: string;
  onStatusChange?: (status: PixStatus) => void;
}) {
  const [data, setData] = useState<PayData | null>(null);
  const [paid, setPaid] = useState(false);
  const [canceled, setCanceled] = useState(false);
  const [err, setErr] = useState("");
  const [copied, setCopied] = useState(false);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState("");

  useEffect(() => {
    api
      .pay(token)
      .then((d) => {
        setData(d);
        setPaid(d.status === "paid");
        setCanceled(d.status === "canceled" || d.status === "expired");
      })
      .catch(() => setErr("Cobrança não encontrada ou expirada."));
  }, [token]);

  useEffect(() => {
    if (!data || paid || canceled || data.status !== "pending") return;
    const es = new EventSource(api.payEventsUrl(token), { withCredentials: true });
    es.addEventListener("paid", () => {
      setPaid(true);
      es.close();
    });
    es.addEventListener("canceled", () => {
      setCanceled(true);
      es.close();
    });
    return () => es.close();
  }, [token, paid, canceled, data]);

  // Reporta o status ao pai (botão do resumo reage a isso).
  useEffect(() => {
    if (!onStatusChange) return;
    if (err) return onStatusChange("error");
    if (paid) return onStatusChange("paid");
    if (canceled) return onStatusChange("unavailable");
    if (!data) return onStatusChange("loading");
    if (data.status === "expired" || data.status === "canceled") return onStatusChange("unavailable");
    onStatusChange("pending");
  }, [err, paid, canceled, data, onStatusChange]);

  function copy() {
    if (!data?.brCode) return;
    navigator.clipboard.writeText(data.brCode);
    setCopied(true);
    setTimeout(() => setCopied(false), 2500);
  }

  async function cancelar() {
    setBusy(true);
    setActionErr("");
    try {
      await api.cancelPay(token);
      setCanceled(true);
    } catch {
      setActionErr("Não foi possível cancelar. Tente novamente.");
    } finally {
      setBusy(false);
    }
  }

  if (err) {
    return (
      <div className="space-y-3 py-2 text-center">
        <XCircle className="mx-auto size-12 text-rose-400" />
        <p className="text-slate-600">{err}</p>
      </div>
    );
  }

  if (paid) {
    return (
      <div className="space-y-4 py-6 text-center">
        <div className="mx-auto grid h-16 w-16 place-items-center rounded-full bg-emerald-100">
          <Check className="size-8 text-emerald-600" />
        </div>
        <h3 className="text-lg font-bold text-[#0e2937]">Pagamento confirmado!</h3>
        <p className="text-sm text-slate-600">Obrigado. Tudo certo com a sua compra.</p>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="flex items-center justify-center gap-2 py-12 text-slate-500">
        <Loader2 className="size-4 animate-spin" /> Gerando seu boleto…
      </div>
    );
  }

  if (canceled || data.status === "expired" || data.status === "canceled") {
    return (
      <div className="space-y-3 py-2 text-center">
        <XCircle className="mx-auto size-12 text-slate-300" />
        <p className="text-slate-600">Esta cobrança não está mais disponível para pagamento.</p>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {data.pdfUrl && (
        <Button
          asChild
          variant="outline"
          className="h-12 w-full gap-2 text-base"
        >
          <a href={data.pdfUrl} target="_blank" rel="noopener noreferrer">
            <FileText className="size-5" /> Baixar boleto (PDF)
          </a>
        </Button>
      )}
      {data.brCode && (
        <div className="flex justify-center rounded-xl border border-slate-100 bg-white p-3">
          <BoletoBarcode linha={data.brCode} className="h-16 w-full" />
        </div>
      )}
      {data.brCode && (
        <div className="space-y-1.5">
          <p className="text-sm text-slate-500">Linha digitável</p>
          <p className="break-all rounded-xl border border-slate-100 bg-slate-50 px-4 py-3 text-center font-mono text-sm text-[#0e2937]">
            {data.brCode}
          </p>
        </div>
      )}
      <Button
        onClick={copy}
        className="h-12 w-full gap-2 bg-[#0db88f] text-base hover:bg-[#0aa17d]"
      >
        {copied ? (
          <>
            <Check className="size-5" /> Código copiado!
          </>
        ) : (
          <>
            <Copy className="size-5" /> Copiar linha digitável
          </>
        )}
      </Button>
      <p className="text-center text-xs text-slate-500">
        Pague pelo app ou internet banking. A compensação pode levar até 3 dias úteis.
      </p>
      <div className="flex items-center justify-center gap-2 text-sm text-amber-600">
        <Loader2 className="size-4 animate-spin" /> Aguardando pagamento…
      </div>
      <button
        onClick={cancelar}
        disabled={busy}
        className="mx-auto block text-sm text-slate-400 underline-offset-2 hover:text-slate-600 hover:underline disabled:opacity-50"
      >
        Cancelar pagamento
      </button>
      {actionErr && <p className="text-center text-sm text-amber-600">{actionErr}</p>}
    </div>
  );
}
