// Injetado sob demanda pelo background (nunca declarativo): lê a seleção,
// desenha o overlay e mostra a resposta. Não fala com a API — quem faz isso é
// o background, que é quem tem os tokens.

if (!window.__quizJevCarregado) {
  window.__quizJevCarregado = true;

  const ID = "__quiz-jev-overlay";
  // Shadow DOM + `all: initial`: sem isso o CSS da página deforma o card, e
  // site de prova costuma ter CSS agressivo.
  const CSS = `
    :host { all: initial; }
    .card {
      position: fixed; z-index: 2147483647; max-width: 340px;
      font: 14px/1.45 system-ui, sans-serif; color: #111;
      background: #fff; border: 1px solid #d4d4d8; border-radius: 10px;
      box-shadow: 0 8px 28px rgba(0,0,0,.18); padding: 12px 14px;
    }
    .linha { display: flex; align-items: baseline; gap: 8px; }
    .letra { font-size: 28px; font-weight: 700; line-height: 1; }
    .texto { flex: 1; }
    .badge { font-size: 11px; text-transform: uppercase; letter-spacing: .04em;
             padding: 2px 6px; border-radius: 999px; background: #e4e4e7; }
    .badge.claude { background: #ddd6fe; }
    .aviso { margin-top: 8px; font-size: 12px; color: #92400e; }
    .motivo { margin-top: 8px; font-size: 13px; color: #3f3f46; }
    .barras { margin-top: 10px; display: grid; gap: 3px; }
    .barra { display: grid; grid-template-columns: 18px 1fr 38px; gap: 6px;
             align-items: center; font-size: 12px; color: #52525b; }
    .barra i { display: block; height: 6px; border-radius: 3px; background: #a1a1aa; }
    .barra.escolhida i { background: #2563eb; }
    .erro { color: #b91c1c; }
    @media (prefers-color-scheme: dark) {
      .card { background: #18181b; color: #fafafa; border-color: #3f3f46; }
      .badge { background: #3f3f46; } .motivo { color: #d4d4d8; }
    }
  `;

  function fechar() {
    document.getElementById(ID)?.remove();
    document.removeEventListener("keydown", aoTeclar, true);
    document.removeEventListener("mousedown", aoClicar, true);
  }

  function aoTeclar(e) {
    if (e.key === "Escape") fechar();
  }

  function aoClicar(e) {
    const host = document.getElementById(ID);
    if (host && !e.composedPath().includes(host)) fechar();
  }

  function abrir(rect) {
    fechar();
    const host = document.createElement("div");
    host.id = ID;
    const shadow = host.attachShadow({ mode: "open" });
    const style = document.createElement("style");
    style.textContent = CSS;
    const card = document.createElement("div");
    card.className = "card";
    // Clamp pro card não sair da tela quando a seleção está no rodapé/borda.
    const topo = Math.min(rect.bottom + 8, window.innerHeight - 180);
    const esq = Math.min(rect.left, window.innerWidth - 360);
    card.style.top = `${Math.max(8, topo)}px`;
    card.style.left = `${Math.max(8, esq)}px`;
    shadow.append(style, card);
    document.body.appendChild(host);
    document.addEventListener("keydown", aoTeclar, true);
    document.addEventListener("mousedown", aoClicar, true);
    return card;
  }

  function barras(probs, escolhida) {
    if (!probs) return "";
    const itens = Object.entries(probs).sort((a, b) => b[1] - a[1]);
    return `<div class="barras">${itens.map(([label, p]) => `
      <div class="barra ${label === escolhida ? "escolhida" : ""}">
        <span>${label}</span><i style="width:${Math.round(p * 100)}%"></i>
        <span>${Math.round(p * 100)}%</span>
      </div>`).join("")}</div>`;
  }

  function mostrarResposta(card, d) {
    const badge = d.source === "claude" ? "claude" : "jev";
    card.innerHTML = `
      <div class="linha">
        <span class="letra">${d.answer}</span>
        <span class="texto">${d.answerText || ""}</span>
        <span class="badge ${badge}">${badge}</span>
      </div>
      ${d.degraded ? `<div class="aviso">Confiança baixa — o segundo modelo não respondeu.</div>` : ""}
      ${d.reasoning ? `<div class="motivo">${d.reasoning}</div>` : ""}
      ${barras(d.probabilities, d.answer)}
    `;
  }

  browser.runtime.onMessage.addListener(async (msg) => {
    if (msg?.type !== "start") return;
    const sel = window.getSelection();
    const raw = sel ? sel.toString().trim() : "";
    if (!raw) return; // sem seleção não chama a API
    const rect = sel.getRangeAt(0).getBoundingClientRect();
    const card = abrir(rect);
    card.textContent = "Consultando…";
    const resp = await browser.runtime.sendMessage({ type: "ask", raw });
    if (!document.getElementById(ID)) return; // usuário fechou enquanto carregava
    if (resp?.ok) mostrarResposta(card, resp.data);
    else card.innerHTML = `<div class="erro">${resp?.error || "falhou"}</div>`;
  });
}
