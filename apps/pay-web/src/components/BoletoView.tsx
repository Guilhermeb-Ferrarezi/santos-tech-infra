import { useEffect, useState } from "react";
import { ArrowLeft, Check, Copy, FileText, Loader2, XCircle } from "lucide-react";
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
  onBack,
}: {
  token: string;
  onStatusChange?: (status: PixStatus) => void;
  // Callback para voltar à seleção de método. Omitir quando não há etapa anterior.
  onBack?: () => void;
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

  // ——— Cabeçalho comum (voltar + título) ———
  const heading = (
    <div className="mb-6 flex items-center gap-3">
      {onBack && (
        <button
          onClick={onBack}
          className="grid size-8 shrink-0 place-items-center rounded-lg text-[#496b84] transition-colors hover:bg-[#eef2f6] hover:text-[#0e2937]"
          aria-label="Voltar"
        >
          <ArrowLeft className="size-4" />
        </button>
      )}
      <div className="flex items-center gap-2">
        <div className="grid size-8 shrink-0 place-items-center rounded-lg bg-[#187abf]/10 text-[#187abf]">
          <FileText className="size-4" aria-hidden />
        </div>
        <h2 className="text-lg font-bold text-[#0e2937]">Pagamento por Boleto</h2>
      </div>
    </div>
  );

  if (err) {
    return (
      <div>
        {heading}
        <div className="space-y-3 py-2 text-center">
          <XCircle className="mx-auto size-12 text-rose-400" />
          <p className="text-[#496b84]">{err}</p>
        </div>
      </div>
    );
  }

  if (paid) {
    return (
      <div>
        {heading}
        <div className="space-y-4 py-6 text-center">
          <div className="mx-auto grid h-16 w-16 place-items-center rounded-full bg-[#0db88f]/15">
            <Check className="size-8 text-[#0db88f]" />
          </div>
          <h3 className="text-lg font-bold text-[#0e2937]">Pagamento confirmado!</h3>
          <p className="text-sm text-[#496b84]">Obrigado. Tudo certo com a sua compra.</p>
        </div>
      </div>
    );
  }

  if (!data) {
    return (
      <div>
        {heading}
        <div className="flex items-center justify-center gap-2 py-12 text-[#496b84]">
          <Loader2 className="size-4 animate-spin" /> Gerando seu boleto…
        </div>
      </div>
    );
  }

  if (canceled || data.status === "expired" || data.status === "canceled") {
    return (
      <div>
        {heading}
        <div className="space-y-3 py-2 text-center">
          <XCircle className="mx-auto size-12 text-[#dbe4ea]" />
          <p className="text-[#496b84]">Esta cobrança não está mais disponível para pagamento.</p>
        </div>
      </div>
    );
  }

  return (
    <div>
      {heading}

      {/* Código de barras ITF */}
      {data.brCode && (
        <div className="mb-5 flex justify-center overflow-hidden rounded-xl border border-[#e3eaf0] bg-white px-4 py-5">
          <BoletoBarcode linha={data.brCode} />
        </div>
      )}

      {/* Linha digitável */}
      {data.brCode && (
        <div className="mb-5 space-y-1.5">
          <p className="text-xs font-medium text-[#496b84]">Linha digitável</p>
          <p className="break-all rounded-xl border border-[#e3eaf0] bg-[#f8fafc] px-4 py-3 text-center font-mono text-sm leading-relaxed text-[#0e2937]">
            {data.brCode}
          </p>
        </div>
      )}

      {/* Botões de ação */}
      <div className="space-y-3">
        <Button
          onClick={copy}
          className="h-12 w-full gap-2 bg-[#0db88f] text-base font-semibold hover:bg-[#0aa17d] active:scale-[0.98]"
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

        {data.pdfUrl && (
          <Button asChild variant="outline" className="h-11 w-full gap-2">
            <a href={data.pdfUrl} target="_blank" rel="noopener noreferrer">
              <FileText className="size-4" /> Baixar boleto (PDF)
            </a>
          </Button>
        )}
      </div>

      {/* Aviso de prazo */}
      <p className="mt-4 text-center text-xs leading-relaxed text-[#496b84]">
        Pague pelo app ou internet banking. A compensação pode levar até{" "}
        <strong className="font-semibold">3 dias úteis</strong>.
      </p>

      {/* Status aguardando */}
      <div className="mt-4 flex items-center justify-center gap-2 rounded-xl border border-amber-100 bg-amber-50 px-4 py-3 text-sm text-amber-700">
        <Loader2 className="size-4 shrink-0 animate-spin" />
        <span>Aguardando confirmação do pagamento…</span>
      </div>

      {/* Cancelar */}
      <div className="mt-4 text-center">
        <button
          onClick={cancelar}
          disabled={busy}
          className="text-sm text-[#496b84] underline-offset-2 hover:text-[#0e2937] hover:underline disabled:opacity-50"
        >
          Cancelar pagamento
        </button>
      </div>

      {actionErr && <p className="mt-2 text-center text-sm text-amber-600">{actionErr}</p>}
    </div>
  );
}
