import type { ReactNode } from "react";
import { ShieldCheck } from "lucide-react";

// CheckoutShell — split layout de duas colunas inspirado no checkout do AbacatePay.
// Esquerda (~60%, fundo branco): o conteúdo da etapa atual.
// Direita (~40%, fundo #F5F8FA): o resumo do pedido fixo + ação principal.
// No mobile as colunas empilham (resumo primeiro, no topo).
export function CheckoutShell({ left, right }: { left: ReactNode; right: ReactNode }) {
  return (
    <div className="min-h-screen w-full bg-white">
      <div className="mx-auto flex min-h-screen w-full max-w-6xl flex-col lg:flex-row">
        {/* Coluna esquerda — conteúdo da etapa (maior, branca) */}
        <main className="order-2 flex-1 px-6 py-8 sm:px-10 sm:py-12 lg:order-1 lg:basis-[60%] lg:py-16">
          <div className="mx-auto w-full max-w-lg">{left}</div>
        </main>

        {/* Coluna direita — resumo do pedido (cinza-claro) */}
        <aside className="order-1 border-b border-[#e3eaf0] bg-[#f5f8fa] px-6 py-8 sm:px-10 lg:order-2 lg:basis-[40%] lg:border-b-0 lg:border-l lg:py-16">
          <div className="mx-auto w-full max-w-sm lg:sticky lg:top-16">{right}</div>
        </aside>
      </div>
    </div>
  );
}

// SecuritySeal — selo "Compra 100% segura" exibido no topo da coluna direita.
export function SecuritySeal() {
  return (
    <div className="mb-6 flex items-center gap-2 text-sm font-medium text-[#0db88f]">
      <ShieldCheck className="size-4" aria-hidden />
      <span>Compra 100% segura</span>
    </div>
  );
}
