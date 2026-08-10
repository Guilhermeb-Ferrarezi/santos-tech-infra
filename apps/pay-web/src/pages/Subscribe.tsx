import { useEffect, useState } from "react";
import { useParams } from "react-router";
import { ArrowRight, Loader2, RotateCcw } from "lucide-react";
import { api, type Product } from "../lib/api";
import { Button } from "../components/ui/button";
import { CheckoutShell } from "../components/CheckoutShell";
import { OrderSummary, type OrderItem } from "../components/OrderSummary";
import { PersonalDataForm } from "../components/PersonalDataForm";
import { validatePayer, emptyPayer, type PayerData } from "../lib/payer";
import { RecView, type RecStatus } from "../components/RecView";
import { onlyDigits } from "../lib/format";

type Step = "data" | "pix";

const FORM_ID = "subscribe-payer-form";

// Checkout de ASSINATURA (PIX Automático) — produto recorrente, item único, fora do
// carrinho. Duas etapas (Dados → Autorização). api.subscribe cria a recorrência e devolve
// o QR de AUTORIZAÇÃO; a tela acompanha a aprovação ao vivo (SSE) → "Assinatura ativa".
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
  const [sub, setSub] = useState<{ token: string; brCode: string; qrCode: string } | null>(null);
  const [recStatus, setRecStatus] = useState<RecStatus>("pending");

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
      setSub({ token: r.token, brCode: r.brCode, qrCode: r.qrCode });
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
          Escaneie no app do seu banco para autorizar a cobrança automática.
        </p>
        {sub && (
          <RecView
            key={sub.token}
            token={sub.token}
            brCode={sub.brCode}
            qrCode={sub.qrCode}
            onStatusChange={setRecStatus}
          />
        )}
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
      <RecAction status={recStatus} submitting={submitting} onRetry={generatePix} />
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

// RecAction — botão/estado da coluna direita durante a etapa de autorização da assinatura.
function RecAction({
  status,
  submitting,
  onRetry,
}: {
  status: RecStatus;
  submitting: boolean;
  onRetry: () => void;
}) {
  if (status === "active") {
    return (
      <div className="rounded-xl bg-emerald-50 px-4 py-3 text-center text-sm font-medium text-emerald-700">
        Assinatura ativa ✓
      </div>
    );
  }
  if (submitting) {
    return (
      <Button disabled className="h-12 w-full gap-2 bg-[#0db88f] text-base">
        <Loader2 className="size-5 animate-spin" /> Trabalhando nisso…
      </Button>
    );
  }
  if (status === "unavailable") {
    // A autorização não foi concluída (recusada/cancelada/expirada) — retentar gera nova.
    return (
      <div className="space-y-2">
        <p className="text-center text-sm text-[#496b84]">Assinatura indisponível</p>
        <Button
          onClick={onRetry}
          className="h-12 w-full gap-2 bg-[#0db88f] text-base hover:bg-[#0aa17d]"
        >
          <RotateCcw className="size-5" /> Tentar novamente
        </Button>
      </div>
    );
  }
  return (
    <div className="flex items-center justify-center gap-2 rounded-xl bg-amber-50 px-4 py-3 text-center text-sm font-medium text-amber-700">
      <Loader2 className="size-4 animate-spin" /> Aguardando autorização…
    </div>
  );
}
