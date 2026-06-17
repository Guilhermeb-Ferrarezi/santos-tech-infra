import { useEffect, useState } from "react";
import { Check, Copy, Loader2, XCircle } from "lucide-react";
import { api, type PayData } from "../lib/api";
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
}: {
  token: string;
  onStatusChange?: (status: PixStatus) => void;
}) {
  const [data, setData] = useState<PayData | null>(null);
  const [paid, setPaid] = useState(false);
  const [err, setErr] = useState("");
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    api
      .pay(token)
      .then((d) => {
        setData(d);
        setPaid(d.status === "paid");
      })
      .catch(() => setErr("Cobrança não encontrada ou expirada."));
  }, [token]);

  useEffect(() => {
    if (!data || paid || data.status !== "pending") return;
    const es = new EventSource(api.payEventsUrl(token), { withCredentials: true });
    es.addEventListener("paid", () => {
      setPaid(true);
      es.close();
    });
    return () => es.close();
  }, [token, paid, data]);

  // Reporta o status ao pai (botão do resumo reage a isso).
  useEffect(() => {
    if (!onStatusChange) return;
    if (err) return onStatusChange("error");
    if (paid) return onStatusChange("paid");
    if (!data) return onStatusChange("loading");
    if (data.status === "expired" || data.status === "canceled") return onStatusChange("unavailable");
    onStatusChange("pending");
  }, [err, paid, data, onStatusChange]);

  function copy() {
    if (!data?.brCode) return;
    navigator.clipboard.writeText(data.brCode);
    setCopied(true);
    setTimeout(() => setCopied(false), 2500);
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
      <div className="space-y-3 py-6 text-center">
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
        <Loader2 className="size-4 animate-spin" /> Gerando seu Pix…
      </div>
    );
  }

  if (data.status === "expired" || data.status === "canceled") {
    return (
      <div className="space-y-3 py-2 text-center">
        <XCircle className="mx-auto size-12 text-slate-300" />
        <p className="text-slate-600">Esta cobrança não está mais disponível para pagamento.</p>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {data.qrCode && (
        <img
          src={data.qrCode}
          alt="QR Code do Pix"
          className="mx-auto h-56 w-56 rounded-xl border border-slate-100"
        />
      )}
      <p className="text-center text-sm text-slate-500">
        Escaneie o QR ou copie o código no app do seu banco.
      </p>
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
            <Copy className="size-5" /> Copiar código PIX
          </>
        )}
      </Button>
      <div className="flex items-center justify-center gap-2 text-sm text-amber-600">
        <Loader2 className="size-4 animate-spin" /> Aguardando pagamento…
      </div>
    </div>
  );
}
