import { useState, type FormEvent } from "react";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Checkbox } from "./ui/checkbox";
import { Button } from "./ui/button";
import { maskCPF, maskPhone, onlyDigits } from "../lib/format";

export function PayerForm({
  onSubmit,
  submitting = false,
}: {
  onSubmit: (taxId: string, phone: string, name: string, email: string, save: boolean) => void;
  submitting?: boolean;
}) {
  const [taxId, setTaxId] = useState("");
  const [phone, setPhone] = useState("");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [save, setSave] = useState(true);
  const [err, setErr] = useState("");

  function handle(e: FormEvent) {
    e.preventDefault();
    const cpf = onlyDigits(taxId);
    const tel = onlyDigits(phone);
    if (!name.trim()) {
      setErr("Informe seu nome completo.");
      return;
    }
    if (!email.trim() || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      setErr("Informe um e-mail válido.");
      return;
    }
    if (cpf.length !== 11) {
      setErr("Informe um CPF válido (11 dígitos).");
      return;
    }
    if (tel && tel.length !== 10 && tel.length !== 11) {
      setErr("Telefone inválido (DDD + número).");
      return;
    }
    setErr("");
    onSubmit(cpf, tel, name.trim(), email.trim(), save);
  }

  return (
    <form className="space-y-4" onSubmit={handle}>
      <div className="space-y-1">
        <Label htmlFor="name">Nome completo</Label>
        <Input id="name" maxLength={100} value={name} onChange={(e) => setName(e.target.value)} placeholder="João da Silva" required />
      </div>
      <div className="space-y-1">
        <Label htmlFor="email">E-mail</Label>
        <Input id="email" type="email" inputMode="email" maxLength={254} value={email} onChange={(e) => setEmail(e.target.value)} placeholder="joao@email.com" required />
      </div>
      <div className="space-y-1">
        <Label htmlFor="cpf">CPF</Label>
        <Input id="cpf" inputMode="numeric" maxLength={14} value={taxId} onChange={(e) => setTaxId(maskCPF(e.target.value))} placeholder="000.000.000-00" required />
      </div>
      <div className="space-y-1">
        <Label htmlFor="tel">Telefone</Label>
        <Input id="tel" inputMode="tel" maxLength={15} value={phone} onChange={(e) => setPhone(maskPhone(e.target.value))} placeholder="(16) 90000-0000" />
      </div>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox checked={save} onCheckedChange={(v) => setSave(Boolean(v))} />
        Salvar meus dados para as próximas compras
      </label>
      {err && <p className="text-sm text-rose-600">{err}</p>}
      <Button type="submit" className="w-full" disabled={submitting}>
        {submitting ? "Gerando…" : "Gerar Pix"}
      </Button>
    </form>
  );
}
