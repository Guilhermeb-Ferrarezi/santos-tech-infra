import { BrowserRouter, Routes, Route } from "react-router-dom";
import ProductPage from "./pages/Product";
import CartPage from "./pages/Cart";
import CheckoutPage from "./pages/Checkout";

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/p/:slug" element={<ProductPage />} />
        <Route path="/pay/:token" element={<CheckoutPage />} />
        {/* Checkout pelo nome do produto: /<slug> (ex.: /teste1) em vez de /cart.
            O carrinho é server-side (api.cart()); o slug é só a URL amigável. */}
        <Route path="/:slug" element={<CartPage />} />
        <Route path="*" element={<CartPage />} />
      </Routes>
    </BrowserRouter>
  );
}
