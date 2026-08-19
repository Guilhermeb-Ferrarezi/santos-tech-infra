import { BellRinging, X } from "@phosphor-icons/react";

// Aviso mandado pelo admin (ver useDeviceHeartbeat) — some ao fechar; não
// reaparece sozinho porque o hook já marcou o id como mostrado.
export function AdminMessageBanner({ text, onDismiss }: { text: string; onDismiss: () => void }) {
  return (
    <div className="flex items-start gap-2 bg-[#0DB88F] px-3 py-2 text-sm text-white">
      <BellRinging className="mt-0.5 size-4 shrink-0" />
      <p className="flex-1">{text}</p>
      <button
        type="button"
        onClick={onDismiss}
        className="shrink-0 rounded p-0.5 text-white/80 transition-colors hover:bg-white/15 hover:text-white"
        title="Fechar aviso"
      >
        <X className="size-4" />
      </button>
    </div>
  );
}
