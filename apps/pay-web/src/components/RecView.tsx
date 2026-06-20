import { useEffect, useState } from "react";
import { Check, Copy, Loader2, XCircle } from "lucide-react";
import { api } from "../lib/api";
import { Button } from "./ui/button";

// Estado da AUTORIZAÇÃO da assinatura (PIX Automático), reportado ao pai.
export type RecStatus = "pending" | "active" | "unavailable";

function mapStatus(s: string): RecStatus {
  if (s === "active") return "active";
  if (s === "pending_auth") return "pending";
  return "unavailable"; // rejected / canceled / expired
}

// RecView — etapa de autorização da assinatura. Mostra o QR/copia-e-cola de autorização
// (PIX Automático, Jornada 2) e acompanha AO VIVO (SSE em /subscribe/{token}/events) até o
// banco aprovar → "Assinatura ativa". Diferente do PixView: não há pagamento imediato; o
// que importa é a autorização do contrato (o débito vem depois, conforme o produto).
export function RecView({
  token,
  brCode,
  qrCode,
  onStatusChange,
}: {
  token: string;
  brCode: string;
  qrCode: string;
  onStatusChange?: (s: RecStatus) => void;
}) {
  const [status, setStatus] = useState<RecStatus>("pending");
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    const es = new EventSource(api.subscribeEventsUrl(token), { withCredentials: true });
    // Snapshot inicial do backend.
    es.addEventListener("status", (e: MessageEvent) => setStatus(mapStatus(e.data)));
    // Transições (o webhook publica quando o pagador autoriza/cancela).
    ["active", "rejected", "canceled", "expired"].forEach((name) =>
      es.addEventListener(name, () => {
        setStatus(mapStatus(name));
        es.close();
      }),
    );
    return () => es.close();
  }, [token]);

  useEffect(() => {
    onStatusChange?.(status);
  }, [status, onStatusChange]);

  function copy() {
    if (!brCode) return;
    navigator.clipboard.writeText(brCode);
    setCopied(true);
    setTimeout(() => setCopied(false), 2500);
  }

  if (status === "active") {
    return (
      <div className="space-y-4 py-6 text-center">
        <div className="mx-auto grid h-16 w-16 place-items-center rounded-full bg-emerald-100">
          <Check className="size-8 text-emerald-600" />
        </div>
        <h3 className="text-lg font-bold text-[#0e2937]">Assinatura ativa!</h3>
        <p className="text-sm text-slate-600">
          Você autorizou a cobrança automática. Pode fechar esta tela.
        </p>
      </div>
    );
  }

  if (status === "unavailable") {
    return (
      <div className="space-y-3 py-2 text-center">
        <XCircle className="mx-auto size-12 text-slate-300" />
        <p className="text-slate-600">Esta assinatura não está mais disponível.</p>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {qrCode && (
        <img
          src={qrCode}
          alt="QR Code da assinatura"
          className="mx-auto h-56 w-56 rounded-xl border border-slate-100"
        />
      )}
      <p className="text-center text-sm text-slate-500">
        Escaneie para autorizar a assinatura no app do seu banco.
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
        <Loader2 className="size-4 animate-spin" /> Aguardando autorização…
      </div>
    </div>
  );
}
