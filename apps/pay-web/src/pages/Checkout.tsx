import { useNavigate, useParams } from "react-router-dom";
import { Card } from "../components/ui/card";
import { PixView } from "../components/PixView";
import { Centered } from "./Product";

// Rota /pay/:token — versão em página cheia (links de pagamento compartilhados).
// O mesmo conteúdo (PixView) é usado no modal do carrinho.
export default function CheckoutPage() {
  const { token = "" } = useParams();
  const nav = useNavigate();
  return (
    <Centered>
      <Card className="p-6 max-w-sm w-full space-y-4">
        <h1 className="text-xl font-bold text-[#0e2937] text-center">Pague com Pix</h1>
        <PixView token={token} onClose={() => nav("/")} />
      </Card>
    </Centered>
  );
}
