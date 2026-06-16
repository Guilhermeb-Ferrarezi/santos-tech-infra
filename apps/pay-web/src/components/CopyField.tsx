import { useState } from "react";
import { Button } from "./ui/button";

export function CopyField({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex gap-2 items-stretch">
      <code className="flex-1 break-all rounded-lg bg-slate-100 p-3 text-xs">{value}</code>
      <Button onClick={() => { navigator.clipboard.writeText(value); setCopied(true); setTimeout(() => setCopied(false), 2000); }}>
        {copied ? "Copiado!" : "Copiar"}
      </Button>
    </div>
  );
}
