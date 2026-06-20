import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { ArrowRight, Loader2 } from "lucide-react";
import { api, type Product } from "../lib/api";
import { Button } from "../components/ui/button";
import { CheckoutShell } from "../components/CheckoutShell";
import { OrderSummary, type OrderItem } from "../components/OrderSummary";
import { PersonalDataForm } from "../components/PersonalDataForm";
import { validatePayer, emptyPayer, type PayerData } from "../lib/payer";
import { PixView, type PixStatus } from "../components/PixView";
import { onlyDigits } from "../lib/format";

type Step = "data" | "pix";

const FORM_ID = "subscribe-payer-form";

// Checkout de ASSINATURA (PIX Automático, Jornada 3) — produto recorrente, item único,
// fora do carrinho. Mesmas 2 etapas do checkout avulso (Dados → Pix), mas chama
// api.subscribe (que cria a recorrência + a 1ª cobr no mesmo QR de autorização).
export default function SubscribePage() {
  const { slug = "" } = useParams();
  const [product, setProduct] = useState<Product | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [step, setStep] = useState<Step>("data");
  const [payer, setPayer] = useState<PayerData>(() => {
    // Pré-preenche com os dados salvos (se existirem e não estiverem expirados).
    // O consentimento é re-afirmado a cada visita (checkbox volta DESMARCADO).
    try {
      const raw = localStorage.getItem("pay_payer");
      if (raw) {
        const d = JSON.parse(raw) as { name?: string; email?: string; phone?: string; exp?: number };
        if (d.exp && d.exp < Date.now()) {
          localStorage.removeItem("pay_payer");
        } else {
          // CPF nunca é salvo (dado sensível) — só nome, e-mail e telefone.
          return { ...emptyPayer, name: d.name ?? "", email: d.email ?? "", phone: d.phone ?? "", save: false };
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
    api.product(slug).then(setProduct).catch(() => setLoadErr("Produto não encontrado"));
  }, [slug]);

  const items: OrderItem[] = product
    ? [{ name: product.name, priceCents: product.priceCents, quantity: 1 }]
    : [];
  const totalCents = product?.priceCents ?? 0;

  async function generatePix() {
    if (!product) return;
    const v = validatePayer(payer);
    if (v) {
      setErr(v);
      return;
    }
    setErr("");
    setSubmitting(true);
    try {
      const r = await api.subscribe(
        product.id,
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
              name: payer.name, email: payer.email, phone: payer.phone, // sem CPF
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
      setErr(e instanceof Error ? e.message : "Erro ao gerar a assinatura");
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
          Preencha seus dados para autorizar a assinatura via PIX.
        </p>
        <PersonalDataForm
          value={payer}
          onChange={setPayer}
          onSubmit={generatePix}
          formId={FORM_ID}
          disabled={submitting}
        />
        {(err || loadErr) && <p className="mt-4 text-sm text-rose-600">{err || loadErr}</p>}
      </div>
    ) : (
      <div>
        <h1 className="text-2xl font-bold text-[#0e2937]">Autorize sua assinatura</h1>
        <p className="mt-1 mb-8 text-sm text-[#496b84]">
          Escaneie para autorizar e pagar a 1ª parcela no app do seu banco.
        </p>
        {pixToken && <PixView token={pixToken} recurring onStatusChange={setPixStatus} />}
        {err && <p className="mt-4 text-sm text-rose-600">{err}</p>}
      </div>
    );

  // ── Coluna direita — ação principal ───────────────────────────────────────
  const action =
    step === "data" ? (
      <Button
        type="submit"
        form={FORM_ID}
        disabled={submitting || !product}
        className="h-12 w-full gap-2 bg-[#0db88f] text-base hover:bg-[#0aa17d]"
      >
        {submitting ? (
          <>
            <Loader2 className="size-5 animate-spin" /> Trabalhando nisso…
          </>
        ) : (
          <>
            Assinar <ArrowRight className="size-5" />
          </>
        )}
      </Button>
    ) : (
      <PixAction status={pixStatus} />
    );

  return (
    <CheckoutShell
      left={left}
      right={
        <OrderSummary
          items={items}
          totalCents={totalCents}
          action={action}
          recurring
          periodicity={product?.periodicity}
        />
      }
    />
  );
}

// PixAction — botão/estado da coluna direita durante a etapa Pix da assinatura.
function PixAction({ status }: { status: PixStatus }) {
  if (status === "paid") {
    return (
      <div className="rounded-xl bg-emerald-50 px-4 py-3 text-center text-sm font-medium text-emerald-700">
        Assinatura ativa ✓
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
      <Loader2 className="size-4 animate-spin" /> Aguardando autorização…
    </div>
  );
}
