/**
 * Link.tsx — checkout de Link de Pagamento reutilizável.
 * Rota: /link/:token
 *
 * Fluxo:
 *  1. Carrega os metadados do link (GET /link/{token}).
 *  2. Pagador preenche dados pessoais + escolhe o método (se mais de um).
 *     • Valor livre (amountCents null) → exibe input de valor no resumo.
 *  3. Ao confirmar:
 *     • PIX / Boleto → payLink → navega para /pay/{charge.publicToken}
 *       (a rota Checkout.tsx já exibe PixView/BoletoView + SSE de status).
 *     • Cartão → CardView já tokeniza + chama api.payCard que POSTa em
 *       /link/{token}/pay internamente. Passamos o token do link via prop.
 */

import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ArrowRight, Loader2, XCircle } from "lucide-react";
import { api, type LinkData } from "../lib/api";
import { Button } from "../components/ui/button";
import { CheckoutShell } from "../components/CheckoutShell";
import { OrderSummary, type OrderItem } from "../components/OrderSummary";
import { PersonalDataForm } from "../components/PersonalDataForm";
import { MethodSelector, type PayMethod } from "../components/MethodSelector";
import { CardView } from "../components/CardView";
import { validatePayer, emptyPayer, type PayerData } from "../lib/payer";
import { onlyDigits } from "../lib/format";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import type { PixStatus } from "../components/PixView";

// ─── Tipos internos ──────────────────────────────────────────────────────────

type PageState =
  | { kind: "loading" }
  | { kind: "inactive" }
  | { kind: "error"; message: string }
  | { kind: "ready"; link: LinkData };

type Step = "data" | "card";

const FORM_ID = "link-payer-form";

// ─── Componente principal ────────────────────────────────────────────────────

export default function LinkPage() {
  const { token = "" } = useParams();
  const nav = useNavigate();

  const [state, setState] = useState<PageState>({ kind: "loading" });
  const [step, setStep] = useState<Step>("data");

  // Método de pagamento selecionado
  const [activeMethod, setActiveMethod] = useState<PayMethod>("pix");

  // Dados do pagador
  const [payer, setPayer] = useState<PayerData>(() => {
    try {
      const raw = localStorage.getItem("pay_payer");
      if (raw) {
        const d = JSON.parse(raw) as { name?: string; email?: string; phone?: string; exp?: number };
        if (d.exp && d.exp < Date.now()) {
          localStorage.removeItem("pay_payer");
        } else {
          return { ...emptyPayer, name: d.name ?? "", email: d.email ?? "", phone: d.phone ?? "", save: false };
        }
      }
    } catch {
      /* ignora localStorage indisponível */
    }
    return emptyPayer;
  });

  // Valor livre (quando link.amountCents === null)
  const [freeAmount, setFreeAmount] = useState("");
  const [freeAmountErr, setFreeAmountErr] = useState("");

  // Estado do submit
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState("");

  // Status para o cartão (usado no resumo)
  const [cardStatus, setCardStatus] = useState<PixStatus>("pending");

  // ── Carrega o link no mount ────────────────────────────────────────────────
  useEffect(() => {
    let alive = true;
    api
      .getLink(token)
      .then((link) => {
        if (!alive) return;
        if (link.status === "inactive") {
          setState({ kind: "inactive" });
          return;
        }
        setState({ kind: "ready", link });
        // Pré-seleciona o único método ou o primeiro
        const methods = link.methods as PayMethod[];
        if (methods.length > 0) setActiveMethod(methods[0]);
      })
      .catch((e: unknown) => {
        if (!alive) return;
        // 410 = link inativo / não encontrado
        const msg = e instanceof Error ? e.message : "";
        if (msg.includes("410") || msg.includes("gone")) {
          setState({ kind: "inactive" });
        } else {
          setState({ kind: "error", message: msg || "Não foi possível carregar o link de pagamento." });
        }
      });
    return () => {
      alive = false;
    };
  }, [token]);

  // ── Helpers ────────────────────────────────────────────────────────────────

  function parsedFreeAmount(): number | null {
    // Aceita "12,50" ou "12.50" ou "1250" (em reais, converte para centavos)
    const clean = freeAmount.replace(/[^\d,.]/g, "").replace(",", ".");
    const v = parseFloat(clean);
    if (isNaN(v) || v <= 0) return null;
    return Math.round(v * 100);
  }

  function savePayer() {
    try {
      if (payer.save) {
        localStorage.setItem(
          "pay_payer",
          JSON.stringify({
            name: payer.name,
            email: payer.email,
            phone: payer.phone, // sem CPF (dado sensível)
            exp: Date.now() + 30 * 24 * 60 * 60 * 1000,
          }),
        );
      } else {
        localStorage.removeItem("pay_payer");
      }
    } catch {
      /* ignora localStorage indisponível */
    }
  }

  // ── Submit do formulário de dados ─────────────────────────────────────────
  async function handleDataSubmit() {
    if (state.kind !== "ready") return;
    const link = state.link;

    // Valida dados do pagador
    const payerErr = validatePayer(payer);
    if (payerErr) { setErr(payerErr); return; }

    // Valida valor livre
    if (link.amountCents === null) {
      const amt = parsedFreeAmount();
      if (!amt || amt <= 0) {
        setFreeAmountErr("Informe um valor maior que zero.");
        return;
      }
      setFreeAmountErr("");
    }

    setErr("");

    // Cartão → muda para etapa do CardView
    if (activeMethod === "card") {
      savePayer();
      setStep("card");
      return;
    }

    // PIX / Boleto → cria a cobrança e redireciona para /pay/{publicToken}
    setSubmitting(true);
    try {
      const amountCents = link.amountCents !== null ? undefined : parsedFreeAmount()!;
      const charge = await api.payLink(token, {
        method: activeMethod,
        ...(amountCents !== undefined ? { amountCents } : {}),
        customer: {
          name: payer.name.trim(),
          taxId: onlyDigits(payer.taxId),
          email: payer.email.trim(),
          phone: onlyDigits(payer.phone),
        },
      });
      savePayer();
      // A página /pay/:token (Checkout.tsx) já exibe PixView/BoletoView + SSE.
      nav(`/pay/${charge.publicToken}`, { replace: true });
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Erro ao gerar cobrança. Tente novamente.");
    } finally {
      setSubmitting(false);
    }
  }

  // ── Renderização ──────────────────────────────────────────────────────────

  // Estados de carregamento / erro / inativo
  if (state.kind === "loading") {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Loader2 className="size-8 animate-spin text-[#0db88f]" />
      </div>
    );
  }

  if (state.kind === "inactive") {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-4 p-8 text-center">
        <div className="mx-auto grid h-16 w-16 place-items-center rounded-full bg-slate-100">
          <XCircle className="size-8 text-slate-400" />
        </div>
        <h1 className="text-xl font-bold text-[#0e2937]">Link indisponível</h1>
        <p className="max-w-sm text-sm text-[#496b84]">
          Este link de pagamento não está mais disponível. Entre em contato com quem o compartilhou.
        </p>
      </div>
    );
  }

  if (state.kind === "error") {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-4 p-8 text-center">
        <div className="mx-auto grid h-16 w-16 place-items-center rounded-full bg-rose-50">
          <XCircle className="size-8 text-rose-400" />
        </div>
        <h1 className="text-xl font-bold text-[#0e2937]">Erro ao carregar</h1>
        <p className="max-w-sm text-sm text-[#496b84]">{state.message}</p>
      </div>
    );
  }

  // state.kind === "ready"
  const link = state.link;
  const methods = link.methods as PayMethod[];
  const isValueFree = link.amountCents === null;
  const displayAmount = isValueFree ? (parsedFreeAmount() ?? 0) : link.amountCents!;

  // Item do resumo
  const items: OrderItem[] = [
    { name: "Pagamento Santos Tech", priceCents: displayAmount, quantity: 1 },
  ];

  // ── Coluna esquerda ───────────────────────────────────────────────────────
  const left =
    step === "card" ? (
      // Etapa de cartão — CardView tokeniza + envia ao backend
      <div>
        <button
          type="button"
          onClick={() => { setStep("data"); setCardStatus("pending"); }}
          className="mb-4 flex items-center gap-1 text-sm text-[#496b84] hover:text-[#0e2937]"
        >
          ← Voltar
        </button>
        <h1 className="text-2xl font-bold text-[#0e2937]">Dados do cartão</h1>
        <p className="mt-1 mb-6 text-sm text-[#496b84]">
          Preencha os dados do cartão para finalizar o pagamento.
        </p>
        <CardView
          token={token}
          totalCents={displayAmount}
          onStatusChange={setCardStatus}
        />
      </div>
    ) : (
      // Etapa de dados pessoais + seleção de método
      <div>
        <h1 className="text-2xl font-bold text-[#0e2937]">Dados do pagamento</h1>
        <p className="mt-1 mb-8 text-sm text-[#496b84]">
          Preencha seus dados e escolha a forma de pagamento.
        </p>

        {/* Seletor de método — sempre visível; se só 1, mostra sem interação */}
        {methods.length > 1 ? (
          <div className="mb-6">
            <p className="mb-3 text-sm font-medium text-[#0e2937]">Forma de pagamento</p>
            <MethodSelector
              methods={methods}
              value={activeMethod}
              onChange={setActiveMethod}
            />
          </div>
        ) : methods.length === 1 ? (
          <div className="mb-6 inline-flex items-center gap-2 rounded-lg border border-[#0db88f] bg-[#0db88f]/10 px-4 py-2 text-sm font-medium text-[#0db88f]">
            {activeMethod === "pix" && "Pagamento via PIX"}
            {activeMethod === "boleto" && "Pagamento via Boleto"}
            {activeMethod === "card" && "Pagamento com Cartão de Crédito"}
          </div>
        ) : null}

        <PersonalDataForm
          value={payer}
          onChange={setPayer}
          onSubmit={handleDataSubmit}
          formId={FORM_ID}
          disabled={submitting}
        />
        {err && <p className="mt-4 text-sm text-rose-600">{err}</p>}
      </div>
    );

  // ── Coluna direita — resumo + ação ────────────────────────────────────────

  // Input de valor livre (fica no resumo, acima do OrderSummary)
  const freeAmountInput = isValueFree && step === "data" ? (
    <div className="mb-4 space-y-1.5">
      <Label htmlFor="free-amount" className="text-sm font-medium text-[#0e2937]">
        Valor a pagar (R$)
      </Label>
      <Input
        id="free-amount"
        inputMode="decimal"
        placeholder="0,00"
        value={freeAmount}
        onChange={(e) => {
          setFreeAmount(e.target.value);
          setFreeAmountErr("");
        }}
        className="h-12 text-base"
      />
      {freeAmountErr && (
        <p className="text-xs text-rose-600">{freeAmountErr}</p>
      )}
    </div>
  ) : null;

  // Ação do resumo
  const action =
    step === "card" ? (
      // Durante o cartão, o CardView controla seu próprio submit
      <CardSummaryAction status={cardStatus} />
    ) : (
      <Button
        type="submit"
        form={FORM_ID}
        disabled={submitting}
        className="h-12 w-full gap-2 bg-[#0db88f] text-base hover:bg-[#0aa17d]"
      >
        {submitting ? (
          <>
            <Loader2 className="size-5 animate-spin" /> Gerando cobrança…
          </>
        ) : activeMethod === "card" ? (
          <>
            Continuar <ArrowRight className="size-5" />
          </>
        ) : (
          <>
            {activeMethod === "pix" ? "Gerar PIX" : "Gerar boleto"}
            <ArrowRight className="size-5" />
          </>
        )}
      </Button>
    );

  return (
    <CheckoutShell
      left={left}
      right={
        <div>
          {freeAmountInput}
          <OrderSummary
            items={items}
            totalCents={displayAmount}
            action={action}
          />
        </div>
      }
    />
  );
}

// ─── Ação do resumo durante pagamento com cartão ──────────────────────────────

function CardSummaryAction({ status }: { status: PixStatus }) {
  if (status === "paid") {
    return (
      <div className="rounded-xl bg-emerald-50 px-4 py-3 text-center text-sm font-medium text-emerald-700">
        Pagamento aprovado ✓
      </div>
    );
  }
  if (status === "loading") {
    return (
      <Button disabled className="h-12 w-full gap-2 bg-[#0db88f] text-base">
        <Loader2 className="size-5 animate-spin" /> Processando…
      </Button>
    );
  }
  if (status === "error") {
    return (
      <div className="rounded-xl bg-rose-50 px-4 py-3 text-center text-sm text-rose-600">
        Cartão recusado
      </div>
    );
  }
  // pending — instrução para preencher o formulário
  return (
    <div className="rounded-xl bg-[#0db88f]/10 px-4 py-3 text-center text-sm font-medium text-[#0db88f]">
      Preencha os dados do cartão para finalizar
    </div>
  );
}

