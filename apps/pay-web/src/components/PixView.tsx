import { useEffect, useState } from "react";
import { Check, Copy, Loader2, XCircle } from "lucide-react";
import { api, type PayData } from "../lib/api";
import { formatBRL } from "../lib/format";
import { Button } from "./ui/button";

// PixView — conteúdo do pagamento Pix, reaproveitado pelo modal (checkout) e pela
// rota /pay/:token (links diretos). Mostra o QR, um único botão de copiar o código,
// e confirma o pagamento ao vivo via SSE.
export function PixView({ token, onClose }: { token: string; onClose: () => void }) {
  const [data, setData] = useState<PayData | null>(null);
  const [paid, setPaid] = useState(false);
  const [err, setErr] = useState("");
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    api.pay(token)
      .then((d) => { setData(d); setPaid(d.status === "paid"); })
      .catch(() => setErr("Cobrança não encontrada ou expirada."));
  }, [token]);

  useEffect(() => {
    if (!data || paid || data.status !== "pending") return;
    const es = new EventSource(api.payEventsUrl(token), { withCredentials: true });
    es.addEventListener("paid", () => { setPaid(true); es.close(); });
    return () => es.close();
  }, [token, paid, data]);

  function copy() {
    if (!data?.brCode) return;
    navigator.clipboard.writeText(data.brCode);
    setCopied(true);
    setTimeout(() => setCopied(false), 2500);
  }

  if (err) {
    return (
      <div className="space-y-5 py-4 text-center">
        <XCircle className="mx-auto size-12 text-rose-400" />
        <p className="text-slate-600">{err}</p>
        <Button variant="outline" className="w-full" onClick={onClose}>Fechar</Button>
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
        <Button className="w-full bg-[#0db88f] hover:bg-[#0aa17d]" onClick={onClose}>Fechar</Button>
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
      <div className="space-y-5 py-4 text-center">
        <XCircle className="mx-auto size-12 text-slate-300" />
        <p className="text-slate-600">Esta cobrança não está mais disponível para pagamento.</p>
        <Button variant="outline" className="w-full" onClick={onClose}>Fechar</Button>
      </div>
    );
  }

  return (
    <div className="space-y-5 text-center">
      <div>
        <div className="text-xs uppercase tracking-wide text-slate-400">Valor</div>
        <div className="text-3xl font-bold text-[#0db88f]">{formatBRL(data.amountCents)}</div>
      </div>
      {data.qrCode && (
        <img src={data.qrCode} alt="QR Code do Pix" className="mx-auto h-52 w-52 rounded-xl border border-slate-100" />
      )}
      <p className="text-sm text-slate-500">Escaneie o QR ou copie o código no app do seu banco.</p>
      <Button onClick={copy} className="h-12 w-full gap-2 bg-[#0db88f] text-base hover:bg-[#0aa17d]">
        {copied ? <><Check className="size-5" /> Código copiado!</> : <><Copy className="size-5" /> Copiar código Pix</>}
      </Button>
      <div className="flex items-center justify-center gap-2 text-sm text-amber-600">
        <Loader2 className="size-4 animate-spin" /> Aguardando pagamento… confirma sozinho aqui.
      </div>
      <Button variant="ghost" className="w-full text-slate-500" onClick={onClose}>Cancelar</Button>
    </div>
  );
}
