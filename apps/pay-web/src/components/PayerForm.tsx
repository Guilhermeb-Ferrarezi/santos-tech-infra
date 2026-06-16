import { useState } from "react";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Checkbox } from "./ui/checkbox";
import { Button } from "./ui/button";

export function PayerForm({ onSubmit }: { onSubmit: (taxId: string, phone: string, save: boolean) => void }) {
  const [taxId, setTaxId] = useState("");
  const [phone, setPhone] = useState("");
  const [save, setSave] = useState(true);
  return (
    <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); onSubmit(taxId, phone, save); }}>
      <div className="space-y-1">
        <Label htmlFor="cpf">CPF</Label>
        <Input id="cpf" value={taxId} onChange={(e) => setTaxId(e.target.value)} placeholder="000.000.000-00" required />
      </div>
      <div className="space-y-1">
        <Label htmlFor="tel">Telefone</Label>
        <Input id="tel" value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="(16) 90000-0000" />
      </div>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox checked={save} onCheckedChange={(v) => setSave(Boolean(v))} />
        Salvar meus dados para as próximas compras
      </label>
      <Button type="submit" className="w-full">Gerar Pix</Button>
    </form>
  );
}
