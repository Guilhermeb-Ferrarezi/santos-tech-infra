import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api, type Product } from "../lib/api";
import { formatBRL } from "../lib/format";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";

export default function ProductPage() {
  const { slug = "" } = useParams();
  const nav = useNavigate();
  const [p, setP] = useState<Product | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => { api.product(slug).then(setP).catch(() => setErr("Produto não encontrado")); }, [slug]);
  if (err) return <Centered>{err}</Centered>;
  if (!p) return <Centered>Carregando…</Centered>;
  return (
    <Centered>
      <Card className="p-6 max-w-md w-full space-y-4">
        <h1 className="text-2xl font-bold text-[#0e2937]">{p.name}</h1>
        <p className="text-slate-600">{p.description}</p>
        <div className="text-3xl font-bold text-[#0db88f]">{formatBRL(p.priceCents)}</div>
        <Button className="w-full" onClick={async () => { await api.addToCart(slug); nav("/cart"); }}>
          Comprar
        </Button>
      </Card>
    </Centered>
  );
}

export function Centered({ children }: { children: React.ReactNode }) {
  return <div className="min-h-screen grid place-items-center p-4">{children}</div>;
}
