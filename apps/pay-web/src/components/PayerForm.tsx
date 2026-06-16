import { useState, type FormEvent } from "react";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Checkbox } from "./ui/checkbox";
import { Button } from "./ui/button";

export function PayerForm({
  onSubmit,
  submitting = false,
}: {
  onSubmit: (taxId: string, phone: string, save: boolean) => void;
  submitting?: boolean;
}) {
  const [taxId, setTaxId] = useState("");
  const [phone, setPhone] = useState("");
  const [save, setSave] = useState(true);
  const [err, setErr] = useState("");

  function handle(e: FormEvent) {
    e.preventDefault();
    const digits = taxId.replace(/\D/g, "");
    if (digits.length !== 11) {
      setErr("Informe um CPF válido (11 dígitos).");
      return;
    }
    setErr("");
    onSubmit(digits, phone.replace(/\D/g, ""), save);
  }

  return (
    <form className="space-y-4" onSubmit={handle}>
      <div className="space-y-1">
        <Label htmlFor="cpf">CPF</Label>
        <Input id="cpf" inputMode="numeric" value={taxId} onChange={(e) => setTaxId(e.target.value)} placeholder="000.000.000-00" required />
      </div>
      <div className="space-y-1">
        <Label htmlFor="tel">Telefone</Label>
        <Input id="tel" inputMode="tel" value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="(16) 90000-0000" />
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
