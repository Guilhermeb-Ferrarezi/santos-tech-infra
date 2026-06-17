import { useEffect, useState } from "react";
import { ArrowRight, Loader2 } from "lucide-react";
import { api, type CartLine } from "../lib/api";
import { Button } from "../components/ui/button";
import { CheckoutShell } from "../components/CheckoutShell";
import { OrderSummary, type OrderItem } from "../components/OrderSummary";
import { PersonalDataForm } from "../components/PersonalDataForm";
import { validatePayer, emptyPayer, type PayerData } from "../lib/payer";
import { PixView, type PixStatus } from "../components/PixView";
import { onlyDigits } from "../lib/format";

type Step = "data" | "pix";

const FORM_ID = "checkout-payer-form";

// Checkout público em 2 etapas, na MESMA página (a coluna direita/resumo permanece):
//   1. "Dados Pessoais" — formulário; botão "Continuar →" no resumo.
//   2. "Pagamento com PIX" — gera a cobrança (api.checkout) e exibe o QR + SSE.
// Sem tela de conta/login no caminho: vai direto de Dados → Pix.
export default function CheckoutPage() {
  const [lines, setLines] = useState<CartLine[] | null>(null);
  const [step, setStep] = useState<Step>("data");
  const [payer, setPayer] = useState<PayerData>(() => {
    // Pré-preenche com os dados salvos (se existirem e não estiverem expirados).
    // O consentimento é re-afirmado a cada visita: o checkbox volta DESMARCADO
    // (save: false) — o usuário decide de novo se quer manter os dados salvos.
    try {
      const raw = localStorage.getItem("pay_payer");
      if (raw) {
        const d = JSON.parse(raw) as { name?: string; email?: string; taxId?: string; phone?: string; exp?: number };
        if (d.exp && d.exp < Date.now()) {
          localStorage.removeItem("pay_payer"); // expirado: descarta
        } else {
          return { ...emptyPayer, name: d.name ?? "", email: d.email ?? "", taxId: d.taxId ?? "", phone: d.phone ?? "", save: false };
        }
      }
    } catch {
      /* ignora localStorage indisponível */
    }
    return emptyPayer;
  });
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState("");
  const [pixToken, setPixToken] = useState<string | null>(null);
  const [pixStatus, setPixStatus] = useState<PixStatus>("loading");

  useEffect(() => {
    api
      .cart()
      .then(setLines)
      .catch(() => setLines([]));
  }, []);

  const items: OrderItem[] = (lines ?? []).map((l) => ({
    name: l.product.name,
    priceCents: l.product.priceCents,
    quantity: l.quantity,
  }));
  const totalCents = items.reduce((s, it) => s + it.priceCents * it.quantity, 0);

  async function generatePix() {
    const v = validatePayer(payer);
    if (v) {
      setErr(v);
      return;
    }
    setErr("");
    setSubmitting(true);
    try {
      const r = await api.checkout(
        onlyDigits(payer.taxId),
        onlyDigits(payer.phone),
        payer.name.trim(),
        payer.email.trim(),
        payer.save,
      );
      setPixToken(r.token);
      setStep("pix");
      // Salva (ou limpa) os dados para as próximas compras conforme o checkbox.
      try {
        if (payer.save) {
          localStorage.setItem(
            "pay_payer",
            JSON.stringify({
              name: payer.name, email: payer.email, taxId: payer.taxId, phone: payer.phone,
              exp: Date.now() + 30 * 24 * 60 * 60 * 1000, // expira em 30 dias
            }),
          );
        } else {
          localStorage.removeItem("pay_payer");
        }
      } catch {
        /* ignora localStorage indisponível */
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Erro ao gerar cobrança");
    } finally {
      setSubmitting(false);
    }
  }

  // ── Coluna esquerda ───────────────────────────────────────────────────────
  const left =
    step === "data" ? (
      <div>
        <h1 className="text-2xl font-bold text-[#0e2937]">Dados Pessoais</h1>
        <p className="mt-1 mb-8 text-sm text-[#496b84]">
          Preencha seus dados para gerar o pagamento via PIX.
        </p>
        <PersonalDataForm
          value={payer}
          onChange={setPayer}
          onSubmit={generatePix}
          formId={FORM_ID}
          disabled={submitting}
        />
        {err && <p className="mt-4 text-sm text-rose-600">{err}</p>}
      </div>
    ) : (
      <div>
        <h1 className="text-2xl font-bold text-[#0e2937]">Pagamento com PIX</h1>
        <p className="mt-1 mb-8 text-sm text-[#496b84]">
          Abra o app do seu banco e pague para confirmar a compra.
        </p>
        {pixToken && <PixView token={pixToken} onStatusChange={setPixStatus} />}
        {err && <p className="mt-4 text-sm text-rose-600">{err}</p>}
      </div>
    );

  // ── Coluna direita — ação principal ───────────────────────────────────────
  const action =
    step === "data" ? (
      <Button
        type="submit"
        form={FORM_ID}
        disabled={submitting}
        className="h-12 w-full gap-2 bg-[#0db88f] text-base hover:bg-[#0aa17d]"
      >
        {submitting ? (
          <>
            <Loader2 className="size-5 animate-spin" /> Trabalhando nisso…
          </>
        ) : (
          <>
            Continuar <ArrowRight className="size-5" />
          </>
        )}
      </Button>
    ) : (
      <PixAction status={pixStatus} />
    );

  return (
    <CheckoutShell
      left={left}
      right={<OrderSummary items={items} totalCents={totalCents} action={action} />}
    />
  );
}

// PixAction — botão/estado da coluna direita durante a etapa Pix.
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
        <Loader2 className="size-5 animate-spin" /> Gerando seu Pix…
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
