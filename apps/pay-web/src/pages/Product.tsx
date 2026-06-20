import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api, type Product } from "../lib/api";
import { formatBRL, formatRecurringBRL } from "../lib/format";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";

export default function ProductPage() {
  const { slug = "" } = useParams();
  const nav = useNavigate();
  const [p, setP] = useState<Product | null>(null);
  const [err, setErr] = useState("");
  const [adding, setAdding] = useState(false);
  useEffect(() => { api.product(slug).then(setP).catch(() => setErr("Produto não encontrado")); }, [slug]);

  async function comprar() {
    // Produto recorrente não entra no carrinho multi-item: vai direto pro checkout
    // de assinatura (item único).
    if (p?.recurring) {
      nav(`/assinar/${slug}`);
      return;
    }
    setAdding(true);
    setErr("");
    try {
      await api.addToCart(slug);
      nav(`/${slug}`);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Erro ao adicionar ao carrinho");
      setAdding(false);
    }
  }

  if (err && !p) return <Centered>{err}</Centered>;
  if (!p) return <Centered>Carregando…</Centered>;
  const recurring = !!p.recurring;
  return (
    <Centered>
      <Card className="p-6 max-w-md w-full space-y-4">
        <h1 className="text-2xl font-bold text-[#0e2937]">{p.name}</h1>
        <p className="text-slate-600">{p.description}</p>
        <div className="text-3xl font-bold text-[#0db88f]">
          {recurring ? formatRecurringBRL(p.priceCents, p.periodicity) : formatBRL(p.priceCents)}
        </div>
        {err && <p className="text-sm text-rose-600">{err}</p>}
        <Button className="w-full" onClick={comprar} disabled={adding}>
          {recurring ? "Assinar" : adding ? "Adicionando…" : "Comprar"}
        </Button>
      </Card>
    </Centered>
  );
}

export function Centered({ children }: { children: React.ReactNode }) {
  return <div className="min-h-screen grid place-items-center p-4">{children}</div>;
}
