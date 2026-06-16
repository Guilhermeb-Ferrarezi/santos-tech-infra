export function SuccessScreen() {
  return (
    <div className="text-center py-12">
      <div className="mx-auto mb-4 grid h-16 w-16 place-items-center rounded-full bg-emerald-100 text-3xl">✅</div>
      <h2 className="text-xl font-bold text-[#0e2937]">Pagamento confirmado!</h2>
      <p className="text-slate-600 mt-2">Obrigado. Você já pode fechar esta página.</p>
    </div>
  );
}
