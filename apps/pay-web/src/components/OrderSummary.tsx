import type { ReactNode } from "react";
import { Package } from "lucide-react";
import { formatBRL } from "../lib/format";
import { SecuritySeal } from "./CheckoutShell";

export interface OrderItem {
  name: string;
  priceCents: number;
  quantity: number;
}

// OrderSummary — conteúdo fixo da coluna direita: selo de segurança, identificação
// do produto, bloco "Resumo" (Subtotal + Total) e o slot da ação principal (action).
export function OrderSummary({
  items,
  totalCents,
  action,
}: {
  items: OrderItem[];
  totalCents: number;
  action?: ReactNode;
}) {
  const main = items[0];
  const extra = items.length - 1;

  return (
    <div>
      <SecuritySeal />

      {/* Identificação do produto */}
      <div className="flex items-center gap-3">
        <div className="grid size-12 shrink-0 place-items-center rounded-xl bg-[#0db88f]/10 text-[#0db88f]">
          <Package className="size-6" aria-hidden />
        </div>
        <div className="min-w-0">
          <div className="truncate font-semibold text-[#0e2937]">
            {main ? main.name : "Compra Santos Tech"}
            {main && main.quantity > 1 ? ` ×${main.quantity}` : ""}
          </div>
          {extra > 0 && (
            <div className="text-xs text-[#496b84]">
              + {extra} {extra === 1 ? "outro item" : "outros itens"}
            </div>
          )}
        </div>
        <div className="ml-auto whitespace-nowrap font-semibold text-[#0e2937]">
          {formatBRL(totalCents)}
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
            <dd>{formatBRL(totalCents)}</dd>
          </div>
          <div className="flex items-baseline justify-between border-t border-[#e3eaf0] pt-3 text-[#0e2937]">
            <dt className="font-semibold">Total</dt>
            <dd className="text-lg font-bold text-[#0db88f]">{formatBRL(totalCents)}</dd>
          </div>
        </dl>
      </div>

      {action && <div className="mt-6">{action}</div>}
    </div>
  );
}
