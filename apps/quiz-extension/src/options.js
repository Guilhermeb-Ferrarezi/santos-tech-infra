// Shim mínimo: Firefox expõe `browser.*`, Chrome expõe só `chrome.*` (ambos
// aceitam promise quando o callback é omitido).
const api = globalThis.browser ?? globalThis.chrome;

const status = document.getElementById("status");
const sessao = document.getElementById("sessao");

async function atualizarSessao() {
  const { loggedIn } = await api.runtime.sendMessage({ type: "status" });
  sessao.textContent = loggedIn ? "Sessão ativa." : "Sem sessão — faça login.";
}

document.getElementById("form").addEventListener("submit", async (e) => {
  e.preventDefault();
  status.textContent = "Entrando…";
  status.className = "";
  const identifier = document.getElementById("identifier").value;
  const password = document.getElementById("password").value;
  const resp = await api.runtime.sendMessage({ type: "login", identifier, password });
  if (resp?.ok) {
    status.textContent = "Pronto.";
    status.className = "ok";
    document.getElementById("password").value = "";
  } else {
    status.textContent = resp?.error || "falhou";
    status.className = "erro";
  }
  atualizarSessao();
});

atualizarSessao();
