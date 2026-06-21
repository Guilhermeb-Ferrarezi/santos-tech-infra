/**
 * CardView — formulário de cartão de crédito com tokenização PCI-DSS browser-side.
 *
 * SEGURANÇA / PCI:
 *   - PAN (número do cartão), CVV e validade ficam APENAS no estado local deste componente.
 *   - Ao submeter, `getCardPaymentToken` envia esses dados DIRETO para a Efí (nunca passam
 *     pelo nosso backend).
 *   - Somente o `paymentToken` (opaco) + dados não-sensíveis (parcelas, titular, endereço)
 *     são enviados para `api.payCard`.
 *   - NUNCA logue, exiba em console ou transmita PAN/CVV/validade para o nosso servidor.
 */

import { useEffect, useRef, useState } from "react";
import { Check, CreditCard, Loader2, RefreshCw, XCircle } from "lucide-react";
import { api } from "../lib/api";
import { getCardPaymentToken, detectBrand, isEfiScriptBlocked } from "../lib/efi";
import { Input } from "./ui/input";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import type { PixStatus } from "./PixView";

// ---------------------------------------------------------------------------
// Helpers de máscara
// ---------------------------------------------------------------------------

function maskCardNumber(raw: string): string {
  const digits = raw.replace(/\D/g, "").slice(0, 16);
  return digits.replace(/(.{4})/g, "$1 ").trimEnd();
}

function maskExpiry(raw: string): string {
  const digits = raw.replace(/\D/g, "").slice(0, 4);
  if (digits.length > 2) return `${digits.slice(0, 2)}/${digits.slice(2)}`;
  return digits;
}

function maskCpf(raw: string): string {
  const d = raw.replace(/\D/g, "").slice(0, 11);
  if (d.length <= 3) return d;
  if (d.length <= 6) return `${d.slice(0, 3)}.${d.slice(3)}`;
  if (d.length <= 9) return `${d.slice(0, 3)}.${d.slice(3, 6)}.${d.slice(6)}`;
  return `${d.slice(0, 3)}.${d.slice(3, 6)}.${d.slice(6, 9)}-${d.slice(9)}`;
}

function maskCep(raw: string): string {
  const d = raw.replace(/\D/g, "").slice(0, 8);
  if (d.length > 5) return `${d.slice(0, 5)}-${d.slice(5)}`;
  return d;
}

function onlyDigits(v: string, max?: number): string {
  const d = v.replace(/\D/g, "");
  return max ? d.slice(0, max) : d;
}

function brandLabel(brand: string): string {
  const map: Record<string, string> = {
    visa: "Visa",
    mastercard: "Mastercard",
    elo: "Elo",
    amex: "Amex",
    hipercard: "Hipercard",
  };
  return map[brand] ?? "";
}

// ---------------------------------------------------------------------------
// Tipos
// ---------------------------------------------------------------------------

interface Installment {
  installments: number;
  value: number; // centavos por parcela
  total: number; // centavos total
  label: string;
}

type CardPhase =
  | "form"        // preenchendo
  | "tokenizing"  // gerando token na Efí
  | "processing"  // aguardando backend / SSE
  | "paid"        // aprovado
  | "declined"    // recusado (cartão)
  | "error";      // erro genérico

interface CardViewProps {
  token: string;
  totalCents: number;
  /** Quando true, exige campo de endereço de cobrança (CEP + número + complemento). */
  requireBillingAddress?: boolean;
  onStatusChange?: (status: PixStatus) => void;
}

// ---------------------------------------------------------------------------
// Componente
// ---------------------------------------------------------------------------

export function CardView({
  token,
  totalCents,
  requireBillingAddress = false,
  onStatusChange,
}: CardViewProps) {
  // --- campos do formulário (dados sensíveis ficam só aqui) ---
  const [cardNumber, setCardNumber] = useState(""); // PAN — nunca vai ao backend
  const [holderName, setHolderName] = useState("");
  const [expiry, setExpiry] = useState(""); // "MM/AA" — nunca vai ao backend
  const [cvv, setCvv] = useState(""); // nunca vai ao backend
  const [holderCpf, setHolderCpf] = useState("");
  const [cep, setCep] = useState("");
  const [addressNum, setAddressNum] = useState("");
  const [complement, setComplement] = useState("");

  // --- estado de parcelas ---
  const [installments, setInstallments] = useState<Installment[]>([]);
  const [selectedInstallment, setSelectedInstallment] = useState(1);
  const [loadingInstallments, setLoadingInstallments] = useState(false);

  // --- estado do fluxo ---
  const [phase, setPhase] = useState<CardPhase>("form");
  const [errorMsg, setErrorMsg] = useState("");
  const [cardMask, setCardMask] = useState(""); // ex.: ****1234

  // --- aviso de script bloqueado ---
  const [scriptBlocked, setScriptBlocked] = useState(false);

  const esRef = useRef<EventSource | null>(null);

  // Detecta bandeira a partir do BIN (primeiros dígitos — sem risco PCI, são públicos)
  const brand = detectBrand(cardNumber);

  // Verifica bloqueio do script Efí ao montar
  useEffect(() => {
    isEfiScriptBlocked().then(setScriptBlocked);
  }, []);

  // Busca parcelas quando a bandeira é conhecida e há valor
  useEffect(() => {
    if (brand === "undefined" || !totalCents) return;
    let alive = true;
    // Loading antes do fetch assíncrono de parcelas — padrão legítimo aqui.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoadingInstallments(true);
    api
      .getInstallments(brand, totalCents)
      .then((list) => {
        if (!alive) return;
        setInstallments(list);
        setSelectedInstallment(list[0]?.installments ?? 1);
      })
      .catch(() => {
        if (!alive) return;
        // Em caso de erro na consulta de parcelas, oferece só 1×
        setInstallments([
          {
            installments: 1,
            value: totalCents,
            total: totalCents,
            label: `1× de ${formatBRL(totalCents)} (sem juros)`,
          },
        ]);
        setSelectedInstallment(1);
      })
      .finally(() => {
        if (alive) setLoadingInstallments(false);
      });
    return () => {
      alive = false;
    };
  }, [brand, totalCents]);

  // Reporta status ao pai (para o botão do resumo)
  useEffect(() => {
    if (!onStatusChange) return;
    if (phase === "paid") return onStatusChange("paid");
    if (phase === "error" || phase === "declined") return onStatusChange("error");
    if (phase === "processing" || phase === "tokenizing") return onStatusChange("loading");
    onStatusChange("pending");
  }, [phase, onStatusChange]);

  // Fecha SSE ao desmontar
  useEffect(() => {
    return () => {
      esRef.current?.close();
    };
  }, []);

  // -------------------------------------------------------------------------
  // Submit
  // -------------------------------------------------------------------------
  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();

    // Validação básica antes de tokenizar
    if (brand === "undefined") {
      setErrorMsg("Bandeira do cartão não reconhecida. Verifique o número.");
      return;
    }
    const [mm, yy] = expiry.split("/");
    if (!mm || !yy || mm.length !== 2 || yy.length !== 2) {
      setErrorMsg("Validade inválida. Use o formato MM/AA.");
      return;
    }
    const cpfDigits = holderCpf.replace(/\D/g, "");
    if (cpfDigits.length !== 11) {
      setErrorMsg("CPF do titular inválido.");
      return;
    }
    if (requireBillingAddress && cep.replace(/\D/g, "").length !== 8) {
      setErrorMsg("CEP inválido.");
      return;
    }

    setErrorMsg("");
    setPhase("tokenizing");

    // --- TOKENIZAÇÃO (dados sensíveis vão DIRETO para a Efí, nunca ao nosso backend) ---
    let paymentToken: string;
    let mask: string;
    try {
      const result = await getCardPaymentToken({
        brand,
        number: cardNumber.replace(/\s/g, ""), // PAN — só para a Efí
        cvv, // CVV — só para a Efí
        expirationMonth: mm, // validade — só para a Efí
        expirationYear: `20${yy}`, // validade — só para a Efí
        holderName,
        holderDocument: cpfDigits,
        reuse: false,
      });
      paymentToken = result.paymentToken;
      mask = result.cardMask;
      // Hardening PCI: assim que temos o token opaco, os dados sensíveis (PAN/CVV/
      // validade) não são mais necessários — zera do estado React imediatamente,
      // independentemente do resultado do pagamento. A máscara (não sensível) é exibida.
      setCardNumber("");
      setCvv("");
      setExpiry("");
    } catch (err: unknown) {
      setPhase("form");
      setErrorMsg(err instanceof Error ? err.message : "Falha ao processar o cartão.");
      return;
    }

    // Salva a máscara para exibição pós-processamento
    setCardMask(mask);
    setPhase("processing");

    // --- ENVIO AO BACKEND: somente dados não-sensíveis + payment_token ---
    try {
      await api.payCard(token, {
        paymentToken, // token opaco da Efí — OK para trafegar
        installments: selectedInstallment,
        holder: holderName,
        holderDocument: cpfDigits,
        ...(requireBillingAddress
          ? { billingAddress: { zipCode: cep.replace(/\D/g, ""), number: addressNum, complement } }
          : {}),
      });
    } catch {
      // Backend retornou erro — pode ser recusa ou erro técnico
      // Aguardamos o SSE para decidir o status real (cartão pode ter sido enviado e está processando)
    }

    // --- SSE: aguarda evento de status final ---
    const es = new EventSource(api.payEventsUrl(token), { withCredentials: true });
    esRef.current = es;

    es.addEventListener("paid", () => {
      setPhase("paid");
      es.close();
    });
    es.addEventListener("unpaid", () => {
      // Recusa da operadora
      setPhase("declined");
      es.close();
    });
    es.addEventListener("canceled", () => {
      setPhase("declined");
      es.close();
    });
    es.onerror = () => {
      // Se o SSE cair sem evento de conclusão, tenta uma consulta direta
      es.close();
      api
        .pay(token)
        .then((d) => {
          if (d.status === "paid") setPhase("paid");
          else if (d.status === "unpaid") setPhase("declined");
          else setPhase("error");
        })
        .catch(() => setPhase("error"));
    };
  }

  // -------------------------------------------------------------------------
  // Renderização por fase
  // -------------------------------------------------------------------------

  if (phase === "paid") {
    return (
      <div className="space-y-4 py-6 text-center">
        <div className="mx-auto grid h-16 w-16 place-items-center rounded-full bg-emerald-100">
          <Check className="size-8 text-emerald-600" />
        </div>
        <h3 className="text-lg font-bold text-[#0e2937]">Pagamento aprovado!</h3>
        <p className="text-sm text-[#496b84]">
          {cardMask ? `Cartão ${cardMask}` : "Cartão de crédito"} — pagamento confirmado.
        </p>
      </div>
    );
  }

  if (phase === "declined") {
    return (
      <div className="space-y-4 py-6 text-center">
        <div className="mx-auto grid h-16 w-16 place-items-center rounded-full bg-rose-100">
          <XCircle className="size-8 text-rose-500" />
        </div>
        <h3 className="text-lg font-bold text-[#0e2937]">Cartão recusado</h3>
        <p className="text-sm text-[#496b84]">
          A operadora não aprovou a transação. Verifique os dados ou tente outro cartão.
        </p>
        <Button
          variant="outline"
          className="mx-auto h-11 gap-2"
          onClick={() => {
            // Limpa dados sensíveis e volta para o formulário
            setCardNumber("");
            setCvv("");
            setExpiry("");
            setHolderName("");
            setHolderCpf("");
            setCep("");
            setAddressNum("");
            setComplement("");
            setPhase("form");
            setErrorMsg("");
          }}
        >
          <RefreshCw className="size-4" /> Tentar outro cartão
        </Button>
      </div>
    );
  }

  if (phase === "tokenizing" || phase === "processing") {
    return (
      <div className="space-y-4 py-10 text-center">
        <div className="mx-auto grid h-16 w-16 place-items-center rounded-full bg-[#0db88f]/10">
          <Loader2 className="size-8 animate-spin text-[#0db88f]" />
        </div>
        <p className="font-medium text-[#0e2937]">
          {phase === "tokenizing" ? "Validando dados do cartão…" : "Processando pagamento…"}
        </p>
        <p className="text-sm text-[#496b84]">Não feche esta janela.</p>
      </div>
    );
  }

  // Fase "form" — formulário principal
  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      {scriptBlocked && (
        <div className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-700">
          Atenção: um bloqueador de scripts pode impedir o processamento do cartão. Desative
          extensões de bloqueio e recarregue a página.
        </div>
      )}

      {/* Número do cartão */}
      <div className="space-y-1.5">
        <Label htmlFor="card-number">Número do cartão</Label>
        <div className="relative">
          <Input
            id="card-number"
            inputMode="numeric"
            autoComplete="cc-number"
            placeholder="1234 5678 9012 3456"
            value={cardNumber}
            onChange={(e) => setCardNumber(maskCardNumber(e.target.value))}
            maxLength={19}
            required
          />
          {brand !== "undefined" && (
            <span className="pointer-events-none absolute right-4 top-1/2 -translate-y-1/2 text-xs font-semibold text-[#187abf]">
              {brandLabel(brand)}
            </span>
          )}
        </div>
      </div>

      {/* Nome no cartão */}
      <div className="space-y-1.5">
        <Label htmlFor="holder-name">Nome no cartão</Label>
        <Input
          id="holder-name"
          autoComplete="cc-name"
          placeholder="Nome igual ao cartão"
          value={holderName}
          onChange={(e) => setHolderName(e.target.value.toUpperCase())}
          maxLength={60}
          required
        />
      </div>

      {/* Validade + CVV */}
      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-1.5">
          <Label htmlFor="expiry">Validade</Label>
          <Input
            id="expiry"
            inputMode="numeric"
            autoComplete="cc-exp"
            placeholder="MM/AA"
            value={expiry}
            onChange={(e) => setExpiry(maskExpiry(e.target.value))}
            maxLength={5}
            required
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="cvv">CVV</Label>
          <Input
            id="cvv"
            inputMode="numeric"
            autoComplete="off"
            placeholder="123"
            value={cvv}
            onChange={(e) => setCvv(onlyDigits(e.target.value, 4))}
            maxLength={4}
            required
          />
        </div>
      </div>

      {/* CPF do titular */}
      <div className="space-y-1.5">
        <Label htmlFor="holder-cpf">CPF do titular</Label>
        <Input
          id="holder-cpf"
          inputMode="numeric"
          autoComplete="off"
          placeholder="000.000.000-00"
          value={holderCpf}
          onChange={(e) => setHolderCpf(maskCpf(e.target.value))}
          maxLength={14}
          required
        />
      </div>

      {/* Endereço de cobrança (opcional por config) */}
      {requireBillingAddress && (
        <>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="cep">CEP</Label>
              <Input
                id="cep"
                inputMode="numeric"
                autoComplete="postal-code"
                placeholder="00000-000"
                value={cep}
                onChange={(e) => setCep(maskCep(e.target.value))}
                maxLength={9}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="address-num">N° Residencial</Label>
              <Input
                id="address-num"
                inputMode="numeric"
                placeholder="000"
                value={addressNum}
                onChange={(e) => setAddressNum(onlyDigits(e.target.value, 10))}
                maxLength={10}
                required
              />
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="complement">Complemento</Label>
            <Input
              id="complement"
              placeholder="Apto, casa, etc."
              value={complement}
              onChange={(e) => setComplement(e.target.value)}
              maxLength={80}
            />
          </div>
        </>
      )}

      {/* Parcelas */}
      <div className="space-y-1.5">
        <Label htmlFor="installments">Parcelas</Label>
        {loadingInstallments ? (
          <div className="flex h-14 items-center gap-2 text-sm text-[#496b84]">
            <Loader2 className="size-4 animate-spin" /> Consultando parcelas…
          </div>
        ) : installments.length > 0 ? (
          <select
            id="installments"
            value={selectedInstallment}
            onChange={(e) => setSelectedInstallment(Number(e.target.value))}
            className="flex h-14 w-full rounded-lg border border-[#e3eaf0] bg-[#f5f8fa] px-4 text-base transition-colors focus:border-[#0db88f] focus:ring-2 focus:ring-[#0db88f]/20 focus:outline-none"
          >
            {installments.map((inst) => (
              <option key={inst.installments} value={inst.installments}>
                {inst.label}
              </option>
            ))}
          </select>
        ) : (
          <div className="flex h-14 items-center gap-2 text-sm text-[#496b84]">
            <CreditCard className="size-4" />
            {brand === "undefined"
              ? "Digite o número do cartão para ver as parcelas."
              : "Parcelas disponíveis após validação."}
          </div>
        )}
      </div>

      {errorMsg && (
        <p className="rounded-lg bg-rose-50 px-4 py-3 text-sm text-rose-600">{errorMsg}</p>
      )}

      {phase === "error" && !errorMsg && (
        <div className="rounded-lg bg-rose-50 px-4 py-3 text-sm text-rose-600">
          Ocorreu um erro ao processar o pagamento. Tente novamente ou escolha outro método.
          <button
            type="button"
            className="ml-2 underline underline-offset-2"
            onClick={() => setPhase("form")}
          >
            Tentar novamente
          </button>
        </div>
      )}

      <Button
        type="submit"
        className="h-12 w-full gap-2 bg-[#0db88f] text-base hover:bg-[#0aa17d]"
        disabled={phase !== "form"}
      >
        <CreditCard className="size-5" /> Pagar com cartão
      </Button>

      <p className="text-center text-xs text-[#496b84]">
        Seus dados de cartão são criptografados e processados com segurança.
      </p>
    </form>
  );
}

// ---------------------------------------------------------------------------
// Helper local (evita importar a lib de formato toda)
// ---------------------------------------------------------------------------
function formatBRL(cents: number): string {
  return (cents / 100).toLocaleString("pt-BR", { style: "currency", currency: "BRL" });
}
