import { useEffect, useState } from "react";
import { api, type CartLine } from "../lib/api";
import { formatBRL } from "../lib/format";
import { Card } from "../components/ui/card";
import { PayerForm } from "../components/PayerForm";
import { PixModal } from "../components/PixModal";
import { Centered } from "./Product";

export default function CartPage() {
  const [lines, setLines] = useState<CartLine[] | null>(null);
  const [err, setErr] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [pixToken, setPixToken] = useState<string | null>(null);

  function loadCart() {
    api.cart().then(setLines).catch(() => { setErr("Não foi possível carregar sua conta."); setLines([]); });
  }
  useEffect(loadCart, []);

  if (lines === null) return <Centered>Carregando…</Centered>;

  const total = lines.reduce((s, l) => s + l.product.priceCents * l.quantity, 0);

  async function checkout(taxId: string, phone: string, name: string, email: string, save: boolean) {
    setSubmitting(true);
    setErr("");
    try {
      const r = await api.checkout(taxId, phone, name, email, save);
      setPixToken(r.token); // abre o modal do Pix, sem trocar de rota
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Erro ao gerar cobrança");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Centered>
      <Card className="p-6 max-w-md w-full space-y-4">
        <h1 className="text-xl font-bold text-[#0e2937]">Sua conta</h1>
        {lines.length === 0 ? (
          <p className="text-slate-600 text-sm">Sua conta está vazia.</p>
        ) : (
          <>
            {lines.map((l) => (
              <div key={l.product.id} className="flex justify-between text-sm">
                <span>{l.product.name} ×{l.quantity}</span><span>{formatBRL(l.product.priceCents * l.quantity)}</span>
              </div>
            ))}
            <div className="flex justify-between font-bold border-t pt-2">
              <span>Total</span><span>{formatBRL(total)}</span>
            </div>
            <PayerForm onSubmit={checkout} submitting={submitting} />
          </>
        )}
        {err && <p className="text-sm text-rose-600">{err}</p>}
      </Card>
      <PixModal token={pixToken} onClose={() => { setPixToken(null); loadCart(); }} />
    </Centered>
  );
}
