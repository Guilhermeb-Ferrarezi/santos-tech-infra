import { useEffect, useState } from "react";
import { ArrowLeft, Check, Copy, Download, Loader2, QrCode, XCircle } from "lucide-react";
import { api, downloadPayReceipt, type PayData } from "../lib/api";
import { Button } from "./ui/button";

// Estado externo do pagamento, reportado ao pai para refletir no botão do resumo.
export type PixStatus = "loading" | "pending" | "paid" | "unavailable" | "error";

// PixView — etapa "Pagamento com PIX" (coluna esquerda do checkout). Mostra o QR,
// o botão "Copiar código PIX" e confirma o pagamento ao vivo via SSE. O valor/total
// e a ação de fechar ficam na coluna direita (resumo); aqui é só o conteúdo do Pix.
// Reaproveitado pela rota /pay/:token (links diretos) e pelo checkout em 2 etapas.
export function PixView({
  token,
  onStatusChange,
  onBack,
  recurring = false,
}: {
  token: string;
  onStatusChange?: (status: PixStatus) => void;
  // Callback para voltar à seleção de método (exibe seta ←). Omitir quando não
  // há etapa anterior (ex.: rota /pay direta).
  onBack?: () => void;
  // Quando true, a 1ª cobr é de uma assinatura (PIX Automático): os rótulos mudam para
  // "autorizar e pagar a 1ª parcela" / "Assinatura ativa".
  recurring?: boolean;
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

  async function baixarComprovante() {
    setBusy(true);
    setActionErr("");
    try {
      await downloadPayReceipt(token);
    } catch {
      setActionErr("O comprovante ainda não está disponível. Tente novamente em instantes.");
    } finally {
      setBusy(false);
    }
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
        <div className="grid size-8 shrink-0 place-items-center rounded-lg bg-[#0db88f]/10 text-[#0db88f]">
          <QrCode className="size-4" aria-hidden />
        </div>
        <h2 className="text-lg font-bold text-[#0e2937]">Pagamento com PIX</h2>
      </div>
    </div>
  );

  if (err) {
    return (
      <div>
        {heading}
        <div className="space-y-3 py-2 text-center">
          <XCircle className="mx-auto size-12 text-rose-400" />
          <p className="text-slate-600">{err}</p>
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
          <h3 className="text-lg font-bold text-[#0e2937]">
            {recurring ? "Assinatura ativa!" : "Pagamento confirmado!"}
          </h3>
          <p className="text-sm text-[#496b84]">
            {recurring
              ? "Sua assinatura está ativa e a 1ª parcela foi paga."
              : "Obrigado. Tudo certo com a sua compra."}
          </p>
          <Button
            variant="outline"
            className="mx-auto h-11 gap-2"
            onClick={baixarComprovante}
            disabled={busy}
          >
            {busy ? <Loader2 className="size-4 animate-spin" /> : <Download className="size-4" />}
            {busy ? "Gerando…" : "Baixar comprovante"}
          </Button>
          {actionErr && <p className="text-sm text-amber-600">{actionErr}</p>}
        </div>
      </div>
    );
  }

  if (!data) {
    return (
      <div>
        {heading}
        <div className="flex items-center justify-center gap-2 py-12 text-[#496b84]">
          <Loader2 className="size-4 animate-spin" /> Gerando seu Pix…
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

      {/* Instruções em 2 passos */}
      <ol className="mb-6 space-y-2">
        <li className="flex items-start gap-3 text-sm text-[#496b84]">
          <span className="mt-0.5 grid size-5 shrink-0 place-items-center rounded-full bg-[#0e2937] text-[10px] font-bold text-white">
            1
          </span>
          <span>
            {recurring
              ? "Abra o app do seu banco e escaneie o QR Code abaixo para autorizar a assinatura."
              : "Abra o app do seu banco e escaneie o QR Code abaixo."}
          </span>
        </li>
        <li className="flex items-start gap-3 text-sm text-[#496b84]">
          <span className="mt-0.5 grid size-5 shrink-0 place-items-center rounded-full bg-[#0e2937] text-[10px] font-bold text-white">
            2
          </span>
          <span>
            Ou copie o código Pix e cole no campo "Pix Copia e Cola" do seu banco.
          </span>
        </li>
      </ol>

      {/* QR Code */}
      {data.qrCode && (
        <div className="mb-5 flex justify-center">
          <div className="rounded-2xl border border-[#e3eaf0] bg-white p-4 shadow-sm">
            <img
              src={data.qrCode}
              alt="QR Code do Pix"
              className="h-52 w-52 rounded-lg"
            />
          </div>
        </div>
      )}

      {/* Botão copiar */}
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
            <Copy className="size-5" /> Copiar código PIX
          </>
        )}
      </Button>

      {/* Status aguardando */}
      <div className="mt-5 flex items-center justify-center gap-2 rounded-xl border border-amber-100 bg-amber-50 px-4 py-3 text-sm text-amber-700">
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
