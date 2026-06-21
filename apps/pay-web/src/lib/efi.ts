/**
 * efi.ts — wrapper de tokenização Efí (PCI-DSS browser-side)
 *
 * FLUXO PCI:
 *   1. O pagador digita os dados do cartão (PAN, CVV, validade) APENAS no browser.
 *   2. Este módulo chama a lib oficial payment-token-efi, que criptografa os dados
 *      e os envia DIRETAMENTE do browser para a Efí (nunca passam pelo nosso backend).
 *   3. A Efí devolve somente um `payment_token` (opaco) e a `card_mask` (ex.: ****1234).
 *   4. Apenas o `payment_token`, parcelas, nome do titular e endereço de cobrança são
 *      enviados ao nosso backend (api.ts) para criar a cobrança.
 *
 * NUNCA logue, armazene ou transmita PAN/CVV/validade para o nosso servidor.
 */

import EfiPay from "payment-token-efi";

// Configuração via env do Vite (definida no .env do projeto).
const PAYEE_CODE = import.meta.env.VITE_EFI_PAYEE_CODE as string;
const EFI_ENV = (import.meta.env.VITE_EFI_ENV ?? "sandbox") as
  | "production"
  | "sandbox";

if (!PAYEE_CODE) {
  console.warn(
    "[efi] VITE_EFI_PAYEE_CODE não definido — tokenização vai falhar em runtime.",
  );
}

// ---------------------------------------------------------------------------
// Tipos públicos
// ---------------------------------------------------------------------------

export interface CardInput {
  /** Bandeira: "visa" | "mastercard" | "elo" | "amex" | "hipercard" */
  brand: string;
  /** PAN completo — NUNCA sai deste módulo; vai direto para a Efí. */
  number: string;
  /** CVV — NUNCA sai deste módulo; vai direto para a Efí. */
  cvv: string;
  /** Mês de validade "MM" (dois dígitos). */
  expirationMonth: string;
  /** Ano de validade "YYYY" (quatro dígitos). */
  expirationYear: string;
  /** Nome impresso no cartão. */
  holderName: string;
  /** CPF ou CNPJ do titular (só dígitos). */
  holderDocument: string;
  /**
   * Se `true`, permite que o token seja reutilizado em cobranças futuras
   * (vaulting). Padrão `false` para cobranças avulsas.
   */
  reuse?: boolean;
}

export interface TokenResult {
  /** Token opaco gerado pela Efí — único dado de cartão que vai ao backend. */
  paymentToken: string;
  /** Máscara para exibição ao usuário (ex.: ****.****.****-1234). */
  cardMask: string;
}

// ---------------------------------------------------------------------------
// Tokenização
// ---------------------------------------------------------------------------

/**
 * Tokeniza os dados do cartão diretamente no browser via Efí.
 *
 * Os campos `number`, `cvv`, `expirationMonth` e `expirationYear` NUNCA saem
 * deste função em texto claro — são passados à lib da Efí que os criptografa
 * antes de qualquer transmissão de rede.
 *
 * @throws Error com mensagem amigável em PT-BR se a tokenização falhar.
 */
export async function getCardPaymentToken(card: CardInput): Promise<TokenResult> {
  try {
    const result = await EfiPay.CreditCard.setAccount(PAYEE_CODE)
      .setEnvironment(EFI_ENV)
      .setCreditCardData({
        brand: card.brand,
        number: card.number,
        cvv: card.cvv,
        expirationMonth: card.expirationMonth,
        expirationYear: card.expirationYear,
        holderName: card.holderName,
        holderDocument: card.holderDocument,
        reuse: card.reuse ?? false,
      })
      .getPaymentToken();

    // A lib retorna PaymentTokenResponse em caso de sucesso ou ErrorResponse em
    // caso de falha. Distinguimos pelo campo `payment_token`.
    if (!("payment_token" in result) || !result.payment_token) {
      const err = result as { code?: string; error?: string; error_description?: string };
      throw new Error(err.error_description ?? err.error ?? "Falha ao tokenizar cartão.");
    }

    return {
      paymentToken: result.payment_token,
      cardMask: result.card_mask,
    };
  } catch (err: unknown) {
    // Re-lança com mensagem amigável; nunca inclui PAN/CVV.
    // `cause` preserva a origem para depuração. O erro da Efí contém apenas
    // metadados (código/mensagem HTTP), nunca PAN/CVV — seguro de encadear.
    if (err instanceof Error) {
      // Mensagem já amigável (vinda do catch interno acima).
      throw new Error(friendlyTokenError(err.message), { cause: err });
    }
    // Objeto de erro da lib Efí (ErrorResponse jogado como throw)
    const apiErr = err as { code?: string; error?: string; error_description?: string };
    throw new Error(friendlyTokenError(apiErr.error_description ?? apiErr.error ?? "Erro desconhecido."), { cause: err });
  }
}

/** Mapeia códigos/mensagens de erro da Efí para PT-BR amigável. */
function friendlyTokenError(raw: string): string {
  const r = raw.toLowerCase();
  if (r.includes("blocked") || r.includes("script")) {
    return "O script de segurança foi bloqueado pelo seu navegador ou extensão. Desative o bloqueador de scripts e tente novamente.";
  }
  if (r.includes("invalid") && r.includes("card")) {
    return "Dados do cartão inválidos. Verifique o número, validade e CVV.";
  }
  if (r.includes("brand") || r.includes("unsupported")) {
    return "Bandeira do cartão não suportada. Aceitamos Visa, Mastercard, Elo, Amex e Hipercard.";
  }
  if (r.includes("account") || r.includes("payee")) {
    return "Configuração de pagamento indisponível. Entre em contato com o suporte.";
  }
  if (r.includes("network") || r.includes("fetch") || r.includes("failed")) {
    return "Falha de conexão ao processar o cartão. Verifique sua internet e tente novamente.";
  }
  // Fallback genérico sem expor detalhe técnico
  return "Não foi possível processar os dados do cartão. Verifique as informações e tente novamente.";
}

// ---------------------------------------------------------------------------
// Detecção de bandeira (local, offline)
// ---------------------------------------------------------------------------

/**
 * Detecta a bandeira do cartão a partir dos primeiros dígitos do PAN.
 * Retorna um dos valores aceitos pela Efí:
 *   "visa" | "mastercard" | "elo" | "amex" | "hipercard" | "undefined"
 *
 * Usado para:
 *   - Exibir o logo da bandeira em tempo real no formulário.
 *   - Alimentar a consulta de parcelas (getInstallments) antes de tokenizar.
 *
 * Observação: esta função recebe só os primeiros dígitos visíveis (BIN) —
 * nunca o PAN completo — então não há risco PCI nesta operação local.
 */
export function detectBrand(number: string): string {
  // Remove espaços e hífen para comparar apenas dígitos.
  const n = number.replace(/\D/g, "");

  if (!n) return "undefined";

  // Amex: começa com 34 ou 37
  if (/^3[47]/.test(n)) return "amex";

  // Hipercard: começa com 606282 ou 3841 (série)
  if (/^606282|^3841[046]/.test(n)) return "hipercard";

  // Elo: conjunto de BINs conhecidos (lista baseada na documentação Efí/Cielo 2025)
  if (
    /^4011(78|79)|^43(1274|8935)|^45(1416|7393|763[12])|^504175|^506(699|7[0-6]\d|77[0-8])|^509\d{3}|^627780|^636(297|368|369)|^6516(5[2-9]|6[0-9]|71)|^6550(41|42)|^6555(2[345]|3[01])/.test(n)
  ) return "elo";

  // Mastercard: 51-55, 2221-2720
  if (/^5[1-5]/.test(n) || /^2(2[2-9]\d|[3-6]\d{2}|7[01]\d|720)/.test(n)) return "mastercard";

  // Visa: começa com 4
  if (/^4/.test(n)) return "visa";

  return "undefined";
}

// ---------------------------------------------------------------------------
// Verificação de bloqueio do script de fingerprint
// ---------------------------------------------------------------------------

/**
 * Verifica se o script de fingerprint da Efí está sendo bloqueado
 * por extensão ou configuração do navegador.
 *
 * Recomendado chamar assim que a tela de cartão montar, para alertar o
 * usuário antes de ele tentar finalizar o pagamento.
 *
 * @returns `true` se bloqueado (checkout pode falhar), `false` se ok.
 */
export async function isEfiScriptBlocked(): Promise<boolean> {
  try {
    return await EfiPay.CreditCard.isScriptBlocked();
  } catch {
    // Se a própria verificação falhar, assumimos bloqueio (fail-safe).
    return true;
  }
}
