import type { ReactNode } from "react";
import { Lock, ShieldCheck } from "lucide-react";

// CheckoutShell — split layout de duas colunas inspirado no checkout do AbacatePay.
// Esquerda (~60%, fundo branco): o conteúdo da etapa atual.
// Direita (~40%, fundo #F5F8FA): o resumo do pedido fixo + ação principal.
// No mobile as colunas empilham (resumo primeiro, no topo).
export function CheckoutShell({ left, right }: { left: ReactNode; right: ReactNode }) {
  return (
    <div className="min-h-screen w-full bg-white">
      <div className="flex min-h-screen w-full flex-col lg:flex-row">
        {/* Coluna esquerda — conteúdo da etapa (maior, branca).
            No mobile fica EM CIMA (order-1); no desktop, à esquerda.
            Conteúdo alinhado à direita (perto da divisa) no desktop. */}
        <main className="order-1 flex-1 px-6 py-8 sm:px-10 sm:py-12 lg:basis-[55%] lg:px-14 lg:py-14">
          <div className="mx-auto w-full max-w-2xl lg:ml-auto lg:mr-0">
            {/* Header: logo à esquerda + selo de segurança à direita */}
            <header className="mb-10 flex items-center justify-between gap-3">
              <div className="flex items-center gap-3">
                <img src="/logo-santostech.svg" alt="Santos Tech" className="h-9 w-9" />
                <span className="text-lg font-bold tracking-tight text-[#0e2937]">Santos Tech</span>
              </div>
              <div className="flex items-center gap-1.5 text-xs font-medium text-[#496b84]">
                <Lock className="size-3.5 text-[#0db88f]" aria-hidden />
                <span>Compra 100% segura</span>
              </div>
            </header>
            {left}
          </div>
        </main>

        {/* Coluna direita — resumo do pedido (cinza-claro), fundo preenchido por
            toda a coluna. No mobile fica EMBAIXO (order-2). */}
        <aside className="order-2 border-t border-[#e3eaf0] bg-[#eef2f6] px-6 py-8 sm:px-10 lg:basis-[45%] lg:border-t-0 lg:border-l lg:px-14 lg:py-16">
          <div className="mx-auto w-full max-w-sm lg:mr-auto lg:ml-0 lg:sticky lg:top-16">{right}</div>
        </aside>
      </div>
    </div>
  );
}

// SecuritySeal — selo "Compra 100% segura" exibido no topo da coluna direita.
export function SecuritySeal() {
  return (
    <div className="mb-5 flex items-center gap-2 text-sm font-medium text-[#0db88f]">
      <ShieldCheck className="size-4" aria-hidden />
      <span>Compra 100% segura</span>
    </div>
  );
}
