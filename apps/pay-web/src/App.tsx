import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import ProductPage from "./pages/Product";
import CartPage from "./pages/Cart";
import CheckoutPage from "./pages/Checkout";

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/p/:slug" element={<ProductPage />} />
        <Route path="/cart" element={<CartPage />} />
        <Route path="/pay/:token" element={<CheckoutPage />} />
        <Route path="*" element={<Navigate to="/cart" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
