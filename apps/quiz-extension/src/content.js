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
      max-height: min(60vh, 420px); overflow-y: auto;
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

  // Reposiciona com a altura REAL do card (não uma estimativa): chamada de
  // novo depois que o conteúdo é populado, porque um card com aviso de
  // degradação + motivo + 4 barras passa fácil de qualquer altura estimada
  // e vazaria do viewport. Prefere abrir ABAIXO da seleção; só sobe quando
  // não couber embaixo. O max-height/overflow-y do CSS é o último recurso
  // pro caso extremo de nem cabendo entre topo e rodapé.
  function posicionar(card, rect) {
    const altura = card.offsetHeight || 40;
    const largura = card.offsetWidth || 340;
    let topo = rect.bottom + 8;
    if (topo + altura > window.innerHeight - 8) {
      const acima = rect.top - 8 - altura;
      topo = acima >= 8 ? acima : Math.max(8, window.innerHeight - altura - 8);
    }
    const esq = Math.min(rect.left, window.innerWidth - largura - 8);
    card.style.top = `${Math.max(8, topo)}px`;
    card.style.left = `${Math.max(8, esq)}px`;
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
    shadow.append(style, card);
    document.body.appendChild(host);
    posicionar(card, rect);
    document.addEventListener("keydown", aoTeclar, true);
    document.addEventListener("mousedown", aoClicar, true);
    return card;
  }

  // Nó de DOM com texto via `textContent` — nunca innerHTML. `d.answerText`,
  // `d.reasoning` e a mensagem de erro vêm da rede (e, na origem, do texto da
  // própria página que o usuário selecionou); tratá-los como HTML permitiria
  // que um "enunciado" hostil injetasse markup com handler de evento
  // (ex. <img onerror=...>) que executaria no contexto do site aberto.
  function el(tag, className, texto) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    if (texto != null) n.textContent = texto;
    return n;
  }

  function limpar(card) {
    card.replaceChildren();
  }

  function barras(probs, escolhida) {
    if (!probs) return null;
    const container = el("div", "barras");
    const itens = Object.entries(probs).sort((a, b) => b[1] - a[1]);
    for (const [label, p] of itens) {
      const pct = Math.round(p * 100); // único valor calculado por nós — numérico, nunca concatenado em markup
      const linha = el("div", `barra${label === escolhida ? " escolhida" : ""}`);
      linha.append(el("span", null, label));
      const barra = document.createElement("i");
      barra.style.width = `${pct}%`;
      linha.append(barra);
      linha.append(el("span", null, `${pct}%`));
      container.append(linha);
    }
    return container;
  }

  function mostrarResposta(card, d, rect) {
    limpar(card);
    const badge = d.source === "claude" ? "claude" : "jev";
    const linha = el("div", "linha");
    linha.append(el("span", "letra", d.answer));
    linha.append(el("span", "texto", d.answerText || ""));
    linha.append(el("span", `badge ${badge}`, badge));
    card.append(linha);
    if (d.degraded) {
      card.append(el("div", "aviso", "Confiança baixa — o segundo modelo não respondeu."));
    }
    // reasoning vem preenchido sempre que a resposta veio do estágio
    // escalado (source: "claude"), não só quando `explain` foi pedido —
    // por isso continua renderizado aqui.
    if (d.reasoning) {
      card.append(el("div", "motivo", d.reasoning));
    }
    const b = barras(d.probabilities, d.answer);
    if (b) card.append(b);
    posicionar(card, rect);
  }

  function mostrarErro(card, mensagem, rect) {
    limpar(card);
    card.append(el("div", "erro", mensagem || "falhou"));
    posicionar(card, rect);
  }

  browser.runtime.onMessage.addListener(async (msg) => {
    if (msg?.type !== "start") return;
    const sel = window.getSelection();
    const raw = sel ? sel.toString().trim() : "";
    if (!raw) return; // sem seleção não chama a API
    const rect = sel.getRangeAt(0).getBoundingClientRect();
    const card = abrir(rect);
    card.textContent = "Consultando…";
    posicionar(card, rect);
    const resp = await browser.runtime.sendMessage({ type: "ask", raw });
    if (!document.getElementById(ID)) return; // usuário fechou enquanto carregava
    if (resp?.ok) mostrarResposta(card, resp.data, rect);
    else mostrarErro(card, resp?.error, rect);
  });
}
