import "./index.css";

// Registrados ANTES de importar App/React: imports estáticos são avaliados
// antes de qualquer código do próprio módulo rodar (mesmo escritos depois no
// arquivo) — só import() dinâmico roda na ordem normal do programa. Sem isso,
// um erro na avaliação de um módulo importado (ex.: SDK do Tauri chamado cedo
// demais, fora de componente) deixava o app com tela branca muda: build
// release não tem devtools acessível, então não tinha pista nenhuma.
function showFatalError(err: unknown) {
  const root = document.getElementById("root");
  if (!root) return;
  const message = err instanceof Error ? `${err.message}\n\n${err.stack ?? ""}` : String(err);
  root.innerHTML = `<pre style="margin:0;padding:16px;background:#0a0a0a;color:#f87171;font:12px/1.5 monospace;white-space:pre-wrap;height:100vh;overflow:auto;box-sizing:border-box;">Santos Hub travou ao iniciar:\n\n${message
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")}</pre>`;
}
window.addEventListener("error", (e) => showFatalError(e.error ?? e.message));
window.addEventListener("unhandledrejection", (e) => showFatalError(e.reason));

(async () => {
  try {
    const [{ default: React }, { default: ReactDOM }, { default: App }, { AuthProvider }] = await Promise.all([
      import("react"),
      import("react-dom/client"),
      import("./App"),
      import("./lib/useAuth"),
    ]);
    ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
      <React.StrictMode>
        <AuthProvider>
          <App />
        </AuthProvider>
      </React.StrictMode>,
    );
  } catch (err) {
    showFatalError(err);
  }
})();
