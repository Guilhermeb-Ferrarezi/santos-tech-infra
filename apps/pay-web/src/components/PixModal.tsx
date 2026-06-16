import { Dialog, DialogContent, DialogHeader, DialogTitle } from "./ui/dialog";
import { PixView } from "./PixView";

// PixModal — abre o pagamento Pix sobre a tela do carrinho, sem trocar de rota.
export function PixModal({ token, onClose }: { token: string | null; onClose: () => void }) {
  return (
    <Dialog open={token !== null} onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>Pague com Pix</DialogTitle>
        </DialogHeader>
        {token && <PixView token={token} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  );
}
