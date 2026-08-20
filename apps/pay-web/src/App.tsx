import { lazy, Suspense } from "react";
import { BrowserRouter, Routes, Route } from "react-router";
import { Loader2 } from "lucide-react";
import ProductPage from "./pages/Product";
import CartPage from "./pages/Cart";
import SubscribePage from "./pages/Subscribe";

// Checkout e Link são as únicas rotas que tocam em cartão/boleto, e por isso
// arrastam as dependências pesadas do app: payment-token-efi (via CardView →
// lib/efi) e jsbarcode (via BoletoView). Importadas de forma estática, esse
// peso ia junto pra quem só abre /p/:slug ou o carrinho — que é a maioria dos
// acessos. React.lazy tira as duas do bundle de entrada.
const CheckoutPage = lazy(() => import("./pages/Checkout"));
const LinkPage = lazy(() => import("./pages/Link"));

function RouteFallback() {
  return (
    <div className="flex min-h-screen items-center justify-center">
      <Loader2 className="size-6 animate-spin text-muted-foreground" />
    </div>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <Suspense fallback={<RouteFallback />}>
        <Routes>
          <Route path="/p/:slug" element={<ProductPage />} />
          {/* Checkout de assinatura (produto recorrente, PIX Automático): item único,
              fora do carrinho. /assinar/<slug>. */}
          <Route path="/assinar/:slug" element={<SubscribePage />} />
          <Route path="/pay/:token" element={<CheckoutPage />} />
          {/* Checkout de Link de Pagamento reutilizável — DEVE vir antes de /:slug
              para que "/link/xyz" não seja capturado pelo catch-all de produto. */}
          <Route path="/link/:token" element={<LinkPage />} />
          {/* Checkout pelo nome do produto: /<slug> (ex.: /teste1) em vez de /cart.
              O carrinho é server-side (api.cart()); o slug é só a URL amigável. */}
          <Route path="/:slug" element={<CartPage />} />
          <Route path="*" element={<CartPage />} />
        </Routes>
      </Suspense>
    </BrowserRouter>
  );
}
