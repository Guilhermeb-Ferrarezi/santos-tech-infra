import { useEffect, useRef, useState } from "react";
import { QRCodeSVG } from "qrcode.react";
import { ArrowClockwise, WarningCircle } from "@phosphor-icons/react";
import { useAuth } from "../lib/useAuth";

interface QRData {
  /**
   * SEGREDO DE POLLING (device_code do RFC 8628). É ele que troca por
   * accessToken+refreshToken em /public/qr-login/poll, então NUNCA pode ser
   * renderizado na tela — nem no QR, nem em texto. Fica só na memória deste
   * componente.
   */
  token: string;
  /**
   * Código PÚBLICO de 6 dígitos (user_code do RFC 8628). É o que vai no QR e
   * na tela: sozinho ele não devolve sessão nenhuma — só serve pra quem JÁ
   * está logado apontar qual pedido de login está autorizando.
   */
  code: string;
  expiresAt: string;
}

// Payload do QR: a página de confirmação com o código PÚBLICO na query.
// Duas razões: (1) segurança — quem fotografar a tela leva só o user_code,
// que não polla nada; (2) usabilidade — a câmera do celular abre a página
// direto, coisa que um token opaco de 64 hex não permitia.
const CONNECT_DEVICE_URL = "https://santos-tech.com/dashboard/conectar-dispositivo";
const qrPayload = (code: string) => `${CONNECT_DEVICE_URL}?code=${encodeURIComponent(code)}`;

function useCountdown(expiresAt: string | null) {
  const [secondsLeft, setSecondsLeft] = useState(0);
  useEffect(() => {
    if (!expiresAt) return;
    const target = new Date(expiresAt).getTime();
    const tick = () => setSecondsLeft(Math.max(0, Math.round((target - Date.now()) / 1000)));
    tick();
    const id = window.setInterval(tick, 1000);
    return () => window.clearInterval(id);
  }, [expiresAt]);
  return secondsLeft;
}

// O Hub GERA e MOSTRA o QR/código (estilo WhatsApp Web) — alguém já logado
// (celular escaneando com a câmera de verdade, ou outra aba) confirma em
// santos-tech.com/dashboard/conectar-dispositivo; aqui só fica com poll até
// aparecer aprovado. Ver useAuth.createQRLogin/pollQRLogin.
export function QRLoginPanel({ onDone }: { onDone: () => void }) {
  const { createQRLogin, pollQRLogin } = useAuth();
  const [data, setData] = useState<QRData | null>(null);
  const [error, setError] = useState(false);
  const pollId = useRef<number | null>(null);
  const secondsLeft = useCountdown(data?.expiresAt ?? null);
  const expired = data != null && secondsLeft <= 0;

  function stopPolling() {
    if (pollId.current !== null) {
      window.clearInterval(pollId.current);
      pollId.current = null;
    }
  }

  async function start() {
    stopPolling();
    setError(false);
    setData(null);
    try {
      const d = await createQRLogin();
      setData(d);
      pollId.current = window.setInterval(async () => {
        try {
          if (await pollQRLogin(d.token)) {
            stopPolling();
            onDone();
          }
        } catch {
          // falha de rede numa rodada de poll não é fatal — tenta de novo no próximo tick
        }
      }, 2000);
    } catch {
      setError(true);
    }
  }

  useEffect(() => {
    start();
    return stopPolling;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // O poll de 2s só parava em sucesso/unmount: quando o código expirava, o app
  // continuava batendo em /public/qr-login/poll pra sempre (o registro no Redis
  // já morreu — resposta é sempre {ready:false}). Para junto com o contador.
  useEffect(() => {
    if (expired) stopPolling();
  }, [expired]);

  const mm = String(Math.floor(secondsLeft / 60)).padStart(2, "0");
  const ss = String(secondsLeft % 60).padStart(2, "0");

  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-white/10 p-3">
      {error && (
        <div className="flex flex-col items-center gap-2 py-4 text-center">
          <WarningCircle className="size-5 text-red-400" />
          <p className="text-xs text-neutral-400">Falha ao gerar o código.</p>
          <button
            type="button"
            onClick={start}
            className="flex items-center gap-1.5 rounded-lg bg-white/10 px-3 py-1.5 text-xs font-medium text-white hover:bg-white/20"
          >
            <ArrowClockwise className="size-3.5" /> Tentar de novo
          </button>
        </div>
      )}

      {!error && !data && <ArrowClockwise className="my-6 size-5 animate-spin text-neutral-500" />}

      {data && (
        <>
          <div className="relative rounded-lg bg-white p-2.5">
            {/* value = user_code público, NUNCA data.token (ver QRData). */}
            <QRCodeSVG value={qrPayload(data.code)} size={140} />
            {expired && (
              <div className="absolute inset-0 grid place-items-center rounded-lg bg-white/90">
                <button
                  type="button"
                  onClick={start}
                  className="flex items-center gap-1.5 rounded-lg bg-[#0DB88F] px-2.5 py-1.5 text-xs font-semibold text-white hover:bg-[#0DB88F]/90"
                >
                  <ArrowClockwise className="size-3.5" /> Gerar novo
                </button>
              </div>
            )}
          </div>

          <div className="text-center">
            <p className="text-[10px] text-neutral-400">Ou digite o código em santos-tech.com/dashboard</p>
            <p className="mt-0.5 font-mono text-lg font-bold tracking-[0.25em] text-white tabular-nums">{data.code}</p>
          </div>

          <p className="flex items-center gap-1.5 text-[11px] text-neutral-500">
            {!expired && <span className="size-1.5 animate-pulse rounded-full bg-[#0DB88F]" />}
            {expired ? "Expirado" : `Aguardando confirmação · ${mm}:${ss}`}
          </p>
        </>
      )}
    </div>
  );
}
