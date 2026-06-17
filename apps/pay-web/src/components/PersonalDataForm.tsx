import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Checkbox } from "./ui/checkbox";
import { maskCPF, maskPhone } from "../lib/format";
import type { PayerData } from "../lib/payer";

// PersonalDataForm — etapa "Dados Pessoais". Estado controlado pelo pai (a página),
// para que o botão "Continuar →" possa viver na coluna direita (resumo).
// O formulário em si não tem botão; o submit é disparado pelo botão do resumo.
export function PersonalDataForm({
  value,
  onChange,
  onSubmit,
  formId,
  disabled = false,
}: {
  value: PayerData;
  onChange: (next: PayerData) => void;
  onSubmit: () => void;
  formId: string;
  disabled?: boolean;
}) {
  const set = (patch: Partial<PayerData>) => onChange({ ...value, ...patch });

  return (
    <form
      id={formId}
      className="space-y-5"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
    >
      <div className="space-y-1.5">
        <Label htmlFor="name">Nome completo</Label>
        <Input
          id="name"
          autoComplete="name"
          maxLength={100}
          value={value.name}
          onChange={(e) => set({ name: e.target.value })}
          placeholder="João da Silva"
          disabled={disabled}
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="cpf">CPF / CNPJ</Label>
        <Input
          id="cpf"
          inputMode="numeric"
          maxLength={14}
          value={value.taxId}
          onChange={(e) => set({ taxId: maskCPF(e.target.value) })}
          placeholder="000.000.000-00"
          disabled={disabled}
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="email">E-mail</Label>
        <Input
          id="email"
          type="email"
          inputMode="email"
          autoComplete="email"
          maxLength={254}
          value={value.email}
          onChange={(e) => set({ email: e.target.value })}
          placeholder="joao@email.com"
          disabled={disabled}
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="tel">Telefone</Label>
        <Input
          id="tel"
          inputMode="tel"
          autoComplete="tel"
          maxLength={15}
          value={value.phone}
          onChange={(e) => set({ phone: maskPhone(e.target.value) })}
          placeholder="(16) 90000-0000"
          disabled={disabled}
        />
      </div>
      <label className="flex items-center gap-2 text-sm text-[#496b84]">
        <Checkbox
          checked={value.save}
          onCheckedChange={(v) => set({ save: Boolean(v) })}
          disabled={disabled}
        />
        Salvar meus dados para as próximas compras
      </label>
    </form>
  );
}
