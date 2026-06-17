import { onlyDigits } from "./format";

export interface PayerData {
  name: string;
  email: string;
  taxId: string;
  phone: string;
  save: boolean;
}

export const emptyPayer: PayerData = { name: "", email: "", taxId: "", phone: "", save: true };

// Valida os dados do pagador. Retorna mensagem de erro (string) ou null se válido.
// Os campos taxId/phone aqui chegam mascarados; a validação usa só os dígitos.
export function validatePayer(d: PayerData): string | null {
  if (!d.name.trim()) return "Informe seu nome completo.";
  if (!d.email.trim() || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(d.email)) return "Informe um e-mail válido.";
  if (onlyDigits(d.taxId).length !== 11) return "Informe um CPF válido (11 dígitos).";
  const tel = onlyDigits(d.phone);
  if (tel && tel.length !== 10 && tel.length !== 11) return "Telefone inválido (DDD + número).";
  return null;
}
