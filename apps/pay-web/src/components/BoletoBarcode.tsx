import { useEffect, useRef } from "react";
import JsBarcode from "jsbarcode";

/**
 * Converte a linha digitável (47 dígitos, boleto bancário) no código de barras de
 * 44 dígitos (FEBRABAN) — o que vai no Interleaved 2 of 5 escaneável. A linha digitável
 * é só uma reorganização do código de barras + dígitos verificadores.
 */
function barcodeFromLinhaDigitavel(linha: string): string | null {
  const d = (linha || "").replace(/\D/g, "");
  if (d.length !== 47) return null;
  const dvGeral = d[32];
  const campo4 = d.slice(33, 47); // fator de vencimento (4) + valor (10)
  const barcode = d.slice(0, 4) + dvGeral + campo4 + d.slice(4, 9) + d.slice(10, 20) + d.slice(21, 31);
  return barcode.length === 44 ? barcode : null;
}

/** Renderiza o código de barras (ITF) do boleto a partir da linha digitável. */
export function BoletoBarcode({ linha, className }: { linha: string; className?: string }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const code = barcodeFromLinhaDigitavel(linha);

  useEffect(() => {
    if (!ref.current || !code) return;
    try {
      JsBarcode(ref.current, code, {
        format: "ITF",
        width: 1.8,
        height: 72,
        displayValue: false,
        margin: 0,
        background: "#ffffff",
        lineColor: "#000000",
      });
    } catch (e) {
      console.error("boleto barcode:", e);
    }
  }, [code]);

  if (!code) return null;
  return (
    <canvas
      ref={ref}
      className={className}
      style={{ maxWidth: "100%", height: "auto" }}
      aria-label="Código de barras do boleto"
    />
  );
}
