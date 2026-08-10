import { BrowserRouter, Routes, Route } from "react-router";
import ProductPage from "./pages/Product";
import CartPage from "./pages/Cart";
import CheckoutPage from "./pages/Checkout";
import SubscribePage from "./pages/Subscribe";
import LinkPage from "./pages/Link";

export default function App() {
  return (
    <BrowserRouter>
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
    </BrowserRouter>
  );
}
