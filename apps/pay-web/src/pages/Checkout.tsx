import { useEffect, useState } from "react";
import { useParams } from "react-router";
import { Loader2, XCircle } from "lucide-react";
import { api } from "../lib/api";
import { Button } from "../components/ui/button";
import { CheckoutShell } from "../components/CheckoutShell";
import { OrderSummary, type OrderItem } from "../components/OrderSummary";
import { PixView, type PixStatus } from "../components/PixView";
import { BoletoView } from "../components/BoletoView";
import { CardView } from "../components/CardView";
import { MethodSelector, type PayMethod } from "../components/MethodSelector";

// Rota /pay/:token — link de pagamento direto (compartilhado). Mostra o mesmo split
// layout do checkout, já na etapa de pagamento (a cobrança já existe). Renderiza PIX,
// boleto ou cartão conforme o método da cobrança ou seleção do usuário.
//
// Quando o backend retorna method="multi" (ou lista de métodos aceitos futuramente),
// o MethodSelector permite ao pagador escolher. Por enquanto derivamos os métodos
// disponíveis a partir do campo `method` da cobrança:
//   - "pix"    → apenas PIX
//   - "boleto" → apenas Boleto
//   - "card"   → apenas Cartão
//   - "multi"  → todos os três (PIX, Boleto, Cartão)
//   - ausente  → PIX (retrocompatibilidade)
export default function PayLinkPage() {
  const { token = "" } = useParams();
  const [amountCents, setAmountCents] = useState(0);
  // availableMethods é preenchido após o fetch; null enquanto carrega.
  const [availableMethods, setAvailableMethods] = useState<PayMethod[] | null>(null);
  // activeMethod é o método atual selecionado pelo usuário (ou o único disponível).
  const [activeMethod, setActiveMethod] = useState<PayMethod>("pix");
  const [loadError, setLoadError] = useState(false);
  const [payStatus, setPayStatus] = useState<PixStatus>("loading");
  const [appliedCoupon, setAppliedCoupon] = useState<string | null>(null);
  const [couponFinalCents, setCouponFinalCents] = useState<number | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .pay(token)
      .then((d) => {
        if (!alive) return;
        setAmountCents(d.amountCents);

        // Deriva os métodos disponíveis do campo `method` da cobrança.
        let methods: PayMethod[];
        if (d.method === "boleto") {
          methods = ["boleto"];
        } else if (d.method === "card") {
          methods = ["card"];
        } else if (d.method === "multi") {
          methods = ["pix", "boleto", "card"];
        } else {
          // "pix" ou ausente (retrocompatibilidade)
          methods = ["pix"];
        }
        setAvailableMethods(methods);
        setActiveMethod(methods[0]);
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

  // Títulos e subtítulos por método
  const methodMeta: Record<PayMethod, { title: string; subtitle: string }> = {
    pix: {
      title: "Pagamento com PIX",
      subtitle: "Abra o app do seu banco e pague para confirmar a compra.",
    },
    boleto: {
      title: "Pagamento por boleto",
      subtitle: "Pague o boleto pelo app ou internet banking para confirmar a compra.",
    },
    card: {
      title: "Pagamento com cartão",
      subtitle: "Preencha os dados do cartão para finalizar a compra.",
    },
  };

  const meta = methodMeta[activeMethod];

  const left = loadError ? (
    <div className="space-y-3 py-8 text-center">
      <XCircle className="mx-auto size-12 text-rose-400" />
      <p className="text-slate-600">Cobrança não encontrada ou indisponível.</p>
    </div>
  ) : availableMethods === null ? (
    <div className="flex items-center justify-center gap-2 py-12 text-slate-500">
      <Loader2 className="size-4 animate-spin" /> Carregando…
    </div>
  ) : (
    <div>
      <h1 className="text-2xl font-bold text-[#0e2937]">Forma de pagamento</h1>
      <p className="mt-1 mb-6 text-sm text-[#496b84]">{meta.subtitle}</p>

      {/* Seletor de método — só aparece quando há mais de um disponível */}
      {availableMethods.length > 1 && (
        <div className="mb-6">
          <MethodSelector
            methods={availableMethods}
            value={activeMethod}
            onChange={(m) => {
              setActiveMethod(m);
              setPayStatus("pending");
            }}
          />
        </div>
      )}

      <h2 className="mb-5 text-lg font-semibold text-[#0e2937]">{meta.title}</h2>

      {/* View do método selecionado */}
      {activeMethod === "pix" && (
        <PixView token={token} onStatusChange={setPayStatus} />
      )}
      {activeMethod === "boleto" && (
        <BoletoView token={token} onStatusChange={setPayStatus} />
      )}
      {activeMethod === "card" && (
        <CardView
          token={token}
          totalCents={couponFinalCents ?? amountCents}
          onStatusChange={setPayStatus}
          coupon={appliedCoupon ?? undefined}
        />
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
          action={<PayAction status={payStatus} method={activeMethod} />}
          allowCoupon={activeMethod === "card" && payStatus !== "paid"}
          onCouponApplied={(code, finalCents) => {
            setAppliedCoupon(code);
            setCouponFinalCents(finalCents);
          }}
        />
      }
    />
  );
}

// ---------------------------------------------------------------------------
// Ação do resumo — varia conforme o status e o método ativo
// ---------------------------------------------------------------------------
function PayAction({ status, method }: { status: PixStatus; method: PayMethod }) {
  if (status === "paid") {
    return (
      <div className="rounded-xl bg-emerald-50 px-4 py-3 text-center text-sm font-medium text-emerald-700">
        {method === "card" ? "Pagamento aprovado ✓" : "Pagamento confirmado ✓"}
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
  if (status === "unavailable") {
    return (
      <div className="rounded-xl bg-slate-100 px-4 py-3 text-center text-sm text-[#496b84]">
        Cobrança indisponível
      </div>
    );
  }
  if (status === "error") {
    return (
      <div className="rounded-xl bg-rose-50 px-4 py-3 text-center text-sm text-rose-600">
        {method === "card" ? "Cartão recusado" : "Cobrança indisponível"}
      </div>
    );
  }
  // pending — mensagem de espera varia conforme o método
  if (method === "card") {
    return (
      <div className="rounded-xl bg-[#0db88f]/10 px-4 py-3 text-center text-sm font-medium text-[#0db88f]">
        Preencha os dados do cartão para finalizar
      </div>
    );
  }
  return (
    <div className="flex items-center justify-center gap-2 rounded-xl bg-amber-50 px-4 py-3 text-center text-sm font-medium text-amber-700">
      <Loader2 className="size-4 animate-spin" /> Aguardando pagamento…
    </div>
  );
}
