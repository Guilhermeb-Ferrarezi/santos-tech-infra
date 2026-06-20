import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { Loader2, XCircle } from "lucide-react";
import { api } from "../lib/api";
import { Button } from "../components/ui/button";
import { CheckoutShell } from "../components/CheckoutShell";
import { OrderSummary, type OrderItem } from "../components/OrderSummary";
import { PixView, type PixStatus } from "../components/PixView";
import { BoletoView } from "../components/BoletoView";

// Rota /pay/:token — link de pagamento direto (compartilhado). Mostra o mesmo split
// layout do checkout, já na etapa de pagamento (a cobrança já existe). Renderiza PIX
// ou boleto conforme o método da cobrança.
export default function PayLinkPage() {
  const { token = "" } = useParams();
  const [amountCents, setAmountCents] = useState(0);
  // method só é conhecido após o fetch. Esperamos resolvê-lo antes de escolher a view —
  // default "pix" renderizaria a linha digitável de um boleto como QR (Pix).
  const [method, setMethod] = useState<"pix" | "boleto" | null>(null);
  const [loadError, setLoadError] = useState(false);
  const [pixStatus, setPixStatus] = useState<PixStatus>("loading");

  useEffect(() => {
    let alive = true;
    api
      .pay(token)
      .then((d) => {
        if (!alive) return;
        setAmountCents(d.amountCents);
        setMethod(d.method === "boleto" ? "boleto" : "pix");
      })
      .catch(() => {
        if (alive) setLoadError(true);
      });
    return () => {
      alive = false;
    };
  }, [token]);

  const items: OrderItem[] = [];
  if (amountCents > 0) {
    items.push({ name: "Compra Santos Tech", priceCents: amountCents, quantity: 1 });
  }

  const isBoleto = method === "boleto";
  const left = loadError ? (
    <div className="space-y-3 py-8 text-center">
      <XCircle className="mx-auto size-12 text-rose-400" />
      <p className="text-slate-600">Cobrança não encontrada ou indisponível.</p>
    </div>
  ) : method === null ? (
    <div className="flex items-center justify-center gap-2 py-12 text-slate-500">
      <Loader2 className="size-4 animate-spin" /> Carregando…
    </div>
  ) : (
    <div>
      <h1 className="text-2xl font-bold text-[#0e2937]">
        {isBoleto ? "Pagamento por boleto" : "Pagamento com PIX"}
      </h1>
      <p className="mt-1 mb-8 text-sm text-[#496b84]">
        {isBoleto
          ? "Pague o boleto pelo app ou internet banking para confirmar a compra."
          : "Abra o app do seu banco e pague para confirmar a compra."}
      </p>
      {isBoleto ? (
        <BoletoView token={token} onStatusChange={setPixStatus} />
      ) : (
        <PixView token={token} onStatusChange={setPixStatus} />
      )}
    </div>
  );

  return (
    <CheckoutShell
      left={left}
      right={
        <OrderSummary
          items={items}
          totalCents={amountCents}
          action={<PixAction status={pixStatus} />}
        />
      }
    />
  );
}

function PixAction({ status }: { status: PixStatus }) {
  if (status === "paid") {
    return (
      <div className="rounded-xl bg-emerald-50 px-4 py-3 text-center text-sm font-medium text-emerald-700">
        Pagamento confirmado ✓
      </div>
    );
  }
  if (status === "loading") {
    return (
      <Button disabled className="h-12 w-full gap-2 bg-[#0db88f] text-base">
        <Loader2 className="size-5 animate-spin" /> Carregando…
      </Button>
    );
  }
  if (status === "unavailable" || status === "error") {
    return (
      <div className="rounded-xl bg-slate-100 px-4 py-3 text-center text-sm text-[#496b84]">
        Cobrança indisponível
      </div>
    );
  }
  return (
    <div className="flex items-center justify-center gap-2 rounded-xl bg-amber-50 px-4 py-3 text-center text-sm font-medium text-amber-700">
      <Loader2 className="size-4 animate-spin" /> Aguardando pagamento…
    </div>
  );
}
