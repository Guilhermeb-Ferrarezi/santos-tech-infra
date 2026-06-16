import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { api, type PayData } from "../lib/api";
import { formatBRL } from "../lib/format";
import { Card } from "../components/ui/card";
import { CopyField } from "../components/CopyField";
import { StatusBadge } from "../components/StatusBadge";
import { SuccessScreen } from "../components/SuccessScreen";
import { Centered } from "./Product";

export default function CheckoutPage() {
  const { token = "" } = useParams();
  const [data, setData] = useState<PayData | null>(null);
  const [paid, setPaid] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    api.pay(token)
      .then((d) => { setData(d); setPaid(d.status === "paid"); })
      .catch(() => setErr("Cobrança não encontrada ou expirada."));
  }, [token]);

  // só abre o SSE depois de ter os dados e enquanto estiver pendente
  useEffect(() => {
    if (!data || paid || data.status !== "pending") return;
    const es = new EventSource(api.payEventsUrl(token), { withCredentials: true });
    es.addEventListener("paid", () => { setPaid(true); es.close(); });
    return () => es.close();
  }, [token, paid, data]);

  if (err) return <Centered><Card className="p-6 max-w-md w-full text-center text-slate-600">{err}</Card></Centered>;
  if (paid) return <Centered><Card className="p-6 max-w-md w-full"><SuccessScreen /></Card></Centered>;
  if (!data) return <Centered>Carregando…</Centered>;
  if (data.status === "expired" || data.status === "canceled") {
    return (
      <Centered>
        <Card className="p-6 max-w-md w-full text-center space-y-2">
          <StatusBadge status={data.status} />
          <p className="text-slate-600">Esta cobrança não está mais disponível para pagamento.</p>
        </Card>
      </Centered>
    );
  }
  return (
    <Centered>
      <Card className="p-6 max-w-md w-full space-y-4 text-center">
        <StatusBadge status={data.status} />
        <div className="text-3xl font-bold text-[#0db88f]">{formatBRL(data.amountCents)}</div>
        {data.qrCode && <img src={data.qrCode} alt="QR Code Pix" className="mx-auto h-56 w-56" />}
        <p className="text-sm text-slate-600">Pague com Pix copia-e-cola:</p>
        <CopyField value={data.brCode} />
        <p className="text-xs text-slate-400">A confirmação aparece aqui automaticamente.</p>
      </Card>
    </Centered>
  );
}
