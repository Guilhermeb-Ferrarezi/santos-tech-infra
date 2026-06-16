export function StatusBadge({ status }: { status: string }) {
  const map: Record<string, string> = {
    pending: "bg-amber-100 text-amber-800",
    paid: "bg-emerald-100 text-emerald-800",
    expired: "bg-rose-100 text-rose-800",
  };
  const label: Record<string, string> = { pending: "Aguardando pagamento", paid: "Pago", expired: "Expirado" };
  return <span className={`rounded-full px-3 py-1 text-xs font-medium ${map[status] ?? "bg-slate-100"}`}>{label[status] ?? status}</span>;
}
