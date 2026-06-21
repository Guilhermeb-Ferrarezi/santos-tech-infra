import { useState, type ReactNode } from "react";
import { Package, RefreshCw, Tag, X } from "lucide-react";
import { formatBRL, formatRecurringBRL } from "../lib/format";
import { SecuritySeal } from "./CheckoutShell";
import { api, type ApplyCouponResult } from "../lib/api";
import { Input } from "./ui/input";
import { Button } from "./ui/button";

export interface OrderItem {
  name: string;
  priceCents: number;
  quantity: number;
  imageUrl?: string;
}

// OrderSummary — conteúdo fixo da coluna direita: selo de segurança, identificação
// do produto, bloco "Resumo" (Subtotal + desconto opcional + Total) e o slot da ação
// principal (action).
// Quando `recurring`, o valor vira "R$ X/mês" (sufixo da `periodicity`) e o resumo
// destaca que é uma assinatura com débito recorrente.
// Quando `allowCoupon`, exibe o campo de cupom abaixo do Subtotal. Quando um cupom
// válido é aplicado, a linha de desconto aparece e o Total reflete o `finalCents`.
// O pai recebe as atualizações via `onCouponApplied(code|null, finalCents|null)`.
export function OrderSummary({
  items,
  totalCents,
  action,
  recurring = false,
  periodicity,
  allowCoupon = false,
  onCouponApplied,
}: {
  items: OrderItem[];
  totalCents: number;
  action?: ReactNode;
  recurring?: boolean;
  periodicity?: string;
  allowCoupon?: boolean;
  onCouponApplied?: (code: string | null, finalCents: number | null) => void;
}) {
  const main = items[0];
  const extra = items.length - 1;
  const price = (cents: number) =>
    recurring ? formatRecurringBRL(cents, periodicity) : formatBRL(cents);

  // ── Estado do cupom ────────────────────────────────────────────────────────
  const [couponCode, setCouponCode] = useState("");
  const [applying, setApplying] = useState(false);
  const [couponResult, setCouponResult] = useState<ApplyCouponResult | null>(null);
  const [couponError, setCouponError] = useState("");

  const appliedDiscount = couponResult?.valid ? couponResult.discountCents ?? 0 : 0;
  const displayTotal = couponResult?.valid && couponResult.finalCents != null
    ? couponResult.finalCents
    : totalCents;

  async function handleApplyCoupon() {
    const code = couponCode.trim().toUpperCase();
    if (!code) return;
    setApplying(true);
    setCouponError("");
    try {
      const res = await api.applyCoupon(code, totalCents);
      setCouponResult(res);
      if (res.valid) {
        setCouponError("");
        onCouponApplied?.(res.code ?? code, res.finalCents ?? totalCents);
      } else {
        setCouponError(res.reason ?? "Cupom inválido");
        onCouponApplied?.(null, null);
      }
    } catch {
      setCouponError("Erro ao validar cupom. Tente novamente.");
      onCouponApplied?.(null, null);
    } finally {
      setApplying(false);
    }
  }

  function handleRemoveCoupon() {
    setCouponCode("");
    setCouponResult(null);
    setCouponError("");
    onCouponApplied?.(null, null);
  }

  return (
    <div>
      <SecuritySeal />

      {/* Foto do produto — só quando o produto tem imagem */}
      {main?.imageUrl && (
        <div className="mb-4 aspect-[16/10] w-full overflow-hidden rounded-2xl border border-[#e3eaf0] bg-white">
          <img src={main.imageUrl} alt={main.name} className="h-full w-full object-cover" />
        </div>
      )}

      {/* Identificação do produto */}
      <div className="flex items-center gap-3 rounded-2xl border border-[#e3eaf0] bg-white p-4">
        {!main?.imageUrl && (
          <div className="grid size-12 shrink-0 place-items-center rounded-xl bg-[#0db88f]/10 text-[#0db88f]">
            <Package className="size-6" aria-hidden />
          </div>
        )}
        <div className="min-w-0 flex-1">
          <div className="truncate text-base font-semibold text-[#0e2937]">
            {main ? main.name : "Compra Santos Tech"}
            {main && main.quantity > 1 ? ` ×${main.quantity}` : ""}
          </div>
          {extra > 0 && (
            <div className="mt-0.5 text-xs text-[#496b84]">
              + {extra} {extra === 1 ? "outro item" : "outros itens"}
            </div>
          )}
          {recurring && (
            <div className="mt-0.5 flex items-center gap-1 text-xs text-[#0db88f]">
              <RefreshCw className="size-3" aria-hidden /> Assinatura
            </div>
          )}
        </div>
        <div className="ml-auto shrink-0 whitespace-nowrap text-base font-semibold text-[#0e2937]">
          {price(totalCents)}
        </div>
      </div>

      {/* Resumo */}
      <div className="mt-4 rounded-xl border border-[#e3eaf0] bg-white px-4 py-4">
        <div className="mb-3 text-[11px] font-semibold uppercase tracking-[0.15em] text-[#496b84]">
          Resumo
        </div>
        <dl className="space-y-2 text-sm">
          <div className="flex justify-between text-[#496b84]">
            <dt>Subtotal</dt>
            <dd>{price(totalCents)}</dd>
          </div>

          {/* Campo de cupom — só quando allowCoupon=true */}
          {allowCoupon && (
            <div className="pt-1">
              {couponResult?.valid ? (
                // Cupom aplicado: mostra linha de desconto com botão remover
                <div className="flex items-center justify-between rounded-lg bg-[#0db88f]/8 px-3 py-2">
                  <span className="flex items-center gap-1.5 text-xs text-[#0db88f]">
                    <Tag className="size-3" aria-hidden />
                    {couponResult.code}
                  </span>
                  <button
                    type="button"
                    onClick={handleRemoveCoupon}
                    className="ml-2 text-[#496b84] transition-colors hover:text-rose-500"
                    aria-label="Remover cupom"
                  >
                    <X className="size-3.5" />
                  </button>
                </div>
              ) : (
                // Input + botão de aplicar
                <div className="flex gap-2">
                  <Input
                    value={couponCode}
                    onChange={(e) => {
                      setCouponCode(e.target.value.toUpperCase());
                      setCouponError("");
                    }}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault();
                        handleApplyCoupon();
                      }
                    }}
                    placeholder="Código do cupom"
                    className="h-9 flex-1 text-sm"
                    disabled={applying}
                    aria-label="Código do cupom"
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={handleApplyCoupon}
                    disabled={applying || !couponCode.trim()}
                    className="h-9 shrink-0 text-xs"
                  >
                    {applying ? "…" : "Aplicar"}
                  </Button>
                </div>
              )}
              {couponError && (
                <p className="mt-1.5 text-xs text-rose-600">{couponError}</p>
              )}
            </div>
          )}

          {/* Linha de desconto — aparece quando cupom válido aplicado */}
          {couponResult?.valid && appliedDiscount > 0 && (
            <div className="flex justify-between text-sm text-[#0db88f]">
              <dt>Desconto ({couponResult.code})</dt>
              <dd>− {formatBRL(appliedDiscount)}</dd>
            </div>
          )}

          <div className="flex items-baseline justify-between border-t border-[#e3eaf0] pt-3">
            <dt className="font-semibold text-[#0e2937]">
              {recurring ? "Valor da assinatura" : "Total"}
            </dt>
            <dd className="text-lg font-bold text-[#0db88f]">{formatBRL(displayTotal)}</dd>
          </div>
        </dl>
        {recurring && (
          <p className="mt-3 rounded-lg bg-[#0db88f]/8 px-3 py-2 text-xs leading-relaxed text-[#496b84]">
            Renovação automática de {price(totalCents)}. Cancele quando quiser no app do seu
            banco.
          </p>
        )}
      </div>

      {action && <div className="mt-5">{action}</div>}
    </div>
  );
}
