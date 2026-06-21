import type { ElementType } from "react";
import { QrCode, FileText, CreditCard } from "lucide-react";
import { cn } from "../lib/utils";

// MethodSelector — radio cards de seleção do método de pagamento.
// Visual inspirado em pixAntes.png: cada método é um card clicável com ícone e rótulo.
// Só renderiza quando o checkout oferece mais de um método (prop `methods`).

export type PayMethod = "pix" | "boleto" | "card";

const METHOD_META: Record<PayMethod, { label: string; icon: ElementType }> = {
  pix: { label: "PIX", icon: QrCode },
  boleto: { label: "Boleto", icon: FileText },
  card: { label: "Cartão de crédito", icon: CreditCard },
};

interface MethodSelectorProps {
  methods: PayMethod[];
  value: PayMethod;
  onChange: (method: PayMethod) => void;
}

export function MethodSelector({ methods, value, onChange }: MethodSelectorProps) {
  if (methods.length <= 1) return null;

  return (
    <div
      role="radiogroup"
      aria-label="Forma de pagamento"
      className="flex flex-wrap gap-3"
    >
      {methods.map((method) => {
        const { label, icon: Icon } = METHOD_META[method];
        const selected = method === value;
        return (
          <button
            key={method}
            type="button"
            role="radio"
            aria-checked={selected}
            onClick={() => onChange(method)}
            className={cn(
              "flex items-center gap-2 rounded-lg border px-4 py-3 text-sm font-medium transition-colors",
              "min-w-[120px] cursor-pointer",
              selected
                ? "border-[#0db88f] bg-[#0db88f]/10 text-[#0db88f]"
                : "border-[#e3eaf0] bg-white text-[#496b84] hover:border-[#0db88f]/50 hover:text-[#0e2937]",
            )}
          >
            <span
              className={cn(
                "flex h-5 w-5 shrink-0 items-center justify-center rounded-full border-2 transition-colors",
                selected ? "border-[#0db88f] bg-[#0db88f]" : "border-[#c8d6e0] bg-white",
              )}
            >
              {selected && (
                <span className="block h-2 w-2 rounded-full bg-white" />
              )}
            </span>
            <Icon className="size-4 shrink-0" aria-hidden />
            {label}
          </button>
        );
      })}
    </div>
  );
}
