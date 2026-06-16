import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, type CartLine } from "../lib/api";
import { formatBRL } from "../lib/format";
import { Card } from "../components/ui/card";
import { PayerForm } from "../components/PayerForm";
import { Centered } from "./Product";

export default function CartPage() {
  const nav = useNavigate();
  const [lines, setLines] = useState<CartLine[]>([]);
  useEffect(() => { api.cart().then(setLines).catch(() => {}); }, []);
  const total = lines.reduce((s, l) => s + l.product.priceCents * l.quantity, 0);
  return (
    <Centered>
      <Card className="p-6 max-w-md w-full space-y-4">
        <h1 className="text-xl font-bold text-[#0e2937]">Sua conta</h1>
        {lines.map((l) => (
          <div key={l.product.id} className="flex justify-between text-sm">
            <span>{l.product.name} ×{l.quantity}</span><span>{formatBRL(l.product.priceCents * l.quantity)}</span>
          </div>
        ))}
        <div className="flex justify-between font-bold border-t pt-2">
          <span>Total</span><span>{formatBRL(total)}</span>
        </div>
        <PayerForm onSubmit={async (taxId, phone, save) => {
          const r = await api.checkout(taxId, phone, save);
          nav(`/pay/${r.token}`);
        }} />
      </Card>
    </Centered>
  );
}
