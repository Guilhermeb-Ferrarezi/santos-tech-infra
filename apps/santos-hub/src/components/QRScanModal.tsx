import { useEffect, useRef, useState } from "react";
import QrScanner from "qr-scanner";
// Vite resolve isso pra URL do arquivo final do worker (bundlado) — sem
// isso, QrScanner tenta buscar o worker num caminho relativo que não existe
// dentro do app empacotado.
import QrScannerWorkerPath from "qr-scanner/qr-scanner-worker.min.js?url";
import { X } from "@phosphor-icons/react";

QrScanner.WORKER_PATH = QrScannerWorkerPath;

export function QRScanModal({ onResult, onClose }: { onResult: (text: string) => void; onClose: () => void }) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!videoRef.current) return;
    const scanner = new QrScanner(
      videoRef.current,
      (result) => {
        scanner.stop();
        onResult(result.data);
      },
      { highlightScanRegion: true, highlightCodeOutline: true },
    );
    scanner.start().catch(() => setError("Não consegui acessar a câmera — confira a permissão do Windows."));
    return () => scanner.destroy();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div className="fixed inset-0 z-[60] flex flex-col items-center justify-center gap-3 bg-black/95 p-6">
      <button
        type="button"
        onClick={onClose}
        title="Cancelar"
        className="absolute right-4 top-4 flex size-8 items-center justify-center rounded-full text-white/60 hover:bg-white/10 hover:text-white"
      >
        <X className="size-4" />
      </button>
      <p className="text-sm text-white/70">Aponte a câmera pro QR code em santos-tech.com/dashboard/conectar-dispositivo</p>
      <video ref={videoRef} className="max-h-[60vh] w-full max-w-sm rounded-lg" muted playsInline />
      {error && <p className="text-sm text-red-400">{error}</p>}
    </div>
  );
}
