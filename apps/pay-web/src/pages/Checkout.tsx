import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { api, type PayData, BASE } from "../lib/api";
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

  useEffect(() => { api.pay(token).then((d) => { setData(d); setPaid(d.status === "paid"); }).catch(() => {}); }, [token]);

  useEffect(() => {
    if (paid) return;
    const es = new EventSource(`${BASE}/pay/${token}/events`, { withCredentials: true });
    es.addEventListener("paid", () => { setPaid(true); es.close(); });
    return () => es.close();
  }, [token, paid]);

  if (paid) return <Centered><Card className="p-6 max-w-md w-full"><SuccessScreen /></Card></Centered>;
  if (!data) return <Centered>Carregando…</Centered>;
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
