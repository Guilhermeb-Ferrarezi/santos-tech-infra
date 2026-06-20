import type { ReactNode } from "react";
import { Package, RefreshCw } from "lucide-react";
import { formatBRL, formatRecurringBRL } from "../lib/format";
import { SecuritySeal } from "./CheckoutShell";

export interface OrderItem {
  name: string;
  priceCents: number;
  quantity: number;
  imageUrl?: string;
}

// OrderSummary — conteúdo fixo da coluna direita: selo de segurança, identificação
// do produto, bloco "Resumo" (Subtotal + Total) e o slot da ação principal (action).
// Quando `recurring`, o valor vira "R$ X/mês" (sufixo da `periodicity`) e o resumo
// destaca que é uma assinatura com débito recorrente.
export function OrderSummary({
  items,
  totalCents,
  action,
  recurring = false,
  periodicity,
}: {
  items: OrderItem[];
  totalCents: number;
  action?: ReactNode;
  recurring?: boolean;
  periodicity?: string;
}) {
  const main = items[0];
  const extra = items.length - 1;
  const price = (cents: number) =>
    recurring ? formatRecurringBRL(cents, periodicity) : formatBRL(cents);

  return (
    <div>
      <SecuritySeal />

      {/* Foto do produto — só quando o produto tem imagem */}
      {main?.imageUrl && (
        <div className="mb-5 aspect-[16/10] w-full overflow-hidden rounded-2xl border border-[#e3eaf0] bg-white">
          <img src={main.imageUrl} alt={main.name} className="h-full w-full object-cover" />
        </div>
      )}

      {/* Identificação do produto */}
      <div className="flex items-center gap-4 rounded-2xl border border-[#e3eaf0] bg-white p-5">
        <div className="grid size-16 shrink-0 place-items-center rounded-2xl bg-[#0db88f]/10 text-[#0db88f]">
          <Package className="size-8" aria-hidden />
        </div>
        <div className="min-w-0">
          <div className="truncate text-lg font-semibold text-[#0e2937]">
            {main ? main.name : "Compra Santos Tech"}
            {main && main.quantity > 1 ? ` ×${main.quantity}` : ""}
          </div>
          {extra > 0 && (
            <div className="mt-0.5 text-sm text-[#496b84]">
              + {extra} {extra === 1 ? "outro item" : "outros itens"}
            </div>
          )}
          {recurring && (
            <div className="mt-0.5 flex items-center gap-1 text-sm text-[#0db88f]">
              <RefreshCw className="size-3.5" aria-hidden /> Assinatura
            </div>
          )}
        </div>
        <div className="ml-auto whitespace-nowrap text-lg font-semibold text-[#0e2937]">
          {price(totalCents)}
        </div>
      </div>

      {/* Resumo */}
      <div className="mt-6 rounded-xl border border-[#e3eaf0] bg-white p-4">
        <div className="mb-3 text-[11px] font-semibold uppercase tracking-[0.15em] text-[#496b84]">
          Resumo
        </div>
        <dl className="space-y-2 text-sm">
          <div className="flex justify-between text-[#496b84]">
            <dt>Subtotal</dt>
            <dd>{price(totalCents)}</dd>
          </div>
          <div className="flex items-baseline justify-between border-t border-[#e3eaf0] pt-3 text-[#0e2937]">
            <dt className="font-semibold">{recurring ? "Valor da assinatura" : "Total"}</dt>
            <dd className="text-lg font-bold text-[#0db88f]">{formatBRL(totalCents)}</dd>
          </div>
        </dl>
        {recurring && (
          <p className="mt-3 text-xs text-[#496b84]">
            Cobrança {price(totalCents)} renovada automaticamente. Você pode cancelar a qualquer
            momento no app do seu banco.
          </p>
        )}
      </div>

      {action && <div className="mt-6">{action}</div>}
    </div>
  );
}
