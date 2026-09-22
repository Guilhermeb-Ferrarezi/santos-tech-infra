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
    .card.aberta { max-width: 420px; }
    .linha { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
    .letra { font-size: 28px; font-weight: 700; line-height: 1; }
    .texto { flex: 1; }
    /* Resposta de questão sem alternativas: sem letra, então o texto vira o
       conteúdo principal — flex-basis 100% derruba badge(s) pra linha de
       baixo, e a entrelinha maior ajuda num parágrafo de até ~600 chars. */
    .texto-aberta { flex-basis: 100%; line-height: 1.55; }
    .badge { font-size: 11px; text-transform: uppercase; letter-spacing: .04em;
             padding: 2px 6px; border-radius: 999px; background: #e4e4e7; }
    .badge.claude { background: #ddd6fe; }
    .badge.imagem { background: #bbf7d0; }
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
      .badge.imagem { background: #14532d; color: #bbf7d0; }
    }
  `;

  // Tags que sempre valem como conteúdo visual, mesmo sem background-image.
  const TAGS_VISUAIS = new Set(["IMG", "CANVAS", "SVG", "TABLE", "MATH", "PICTURE", "VIDEO", "FIGURE"]);
  // Abaixo disso é ícone, spacer ou pixel de tracking — não vale a pena virar
  // chamada de visão por causa de uma estrelinha de "favoritar".
  const TAMANHO_MINIMO_PX = 40;
  const LIMITE_BYTES_IMAGEM = 7 * 1024 * 1024; // margem para o teto de 8MB do servidor
  const FOLGA_RECORTE_PX = 8;

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

  function mostrarResposta(card, d, rect, comImagem) {
    limpar(card);
    // Questão sem alternativas: `kind === "aberta"` é o sinal oficial, mas
    // também cobrimos `answer` vazio — cinturão e suspensório pro caso de
    // alguém recarregar a extensão antes do backend novo subir.
    const aberta = d.kind === "aberta" || !d.answer;
    card.classList.toggle("aberta", aberta);
    const badge = d.source === "claude" ? "claude" : "jev";
    const linha = el("div", "linha");
    if (aberta) {
      // Sem letra — não há alternativa nenhuma, e um traço no lugar só
      // confundiria. O texto da resposta é o conteúdo principal aqui.
      linha.append(el("span", "texto texto-aberta", d.answerText || ""));
      linha.append(el("span", `badge ${badge}`, badge));
    } else {
      linha.append(el("span", "letra", d.answer));
      linha.append(el("span", "texto", d.answerText || ""));
      linha.append(el("span", `badge ${badge}`, badge));
    }
    // Resposta com imagem não traz probabilities (não houve veredito do
    // modelo rápido) — badge extra deixa claro que a figura foi considerada,
    // já que o usuário não tem outro sinal disso no card.
    if (comImagem) linha.append(el("span", "badge imagem", "figura"));
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

  function elementoVisualRelevante(elemento) {
    const temTagVisual = TAGS_VISUAIS.has(elemento.tagName);
    if (!temTagVisual) {
      const bg = getComputedStyle(elemento).backgroundImage;
      if (!bg || bg === "none") return false;
    }
    const r = elemento.getBoundingClientRect();
    // Descarta candidato minúsculo (ícone, spacer, pixel de tracking) — sem
    // isso qualquer estrelinha de "favoritar" na página vira chamada de visão.
    return r.width >= TAMANHO_MINIMO_PX && r.height >= TAMANHO_MINIMO_PX;
  }

  // Procura só dentro do ancestral comum da seleção — nunca no documento
  // inteiro — e confirma com range.intersectsNode() que o candidato
  // realmente faz parte do que foi selecionado, não só está por perto.
  function encontrarElementosVisuais(range) {
    let raiz = range.commonAncestorContainer;
    if (raiz.nodeType !== Node.ELEMENT_NODE) raiz = raiz.parentElement;
    if (!raiz) return [];
    const candidatos = [];
    if (elementoVisualRelevante(raiz) && range.intersectsNode(raiz)) candidatos.push(raiz);
    for (const elemento of raiz.querySelectorAll("*")) {
      if (elementoVisualRelevante(elemento) && range.intersectsNode(elemento)) candidatos.push(elemento);
    }
    return candidatos;
  }

  // União dos retângulos (seleção + elementos visuais) com uma folga, em
  // coordenadas de viewport (mesmo espaço de getBoundingClientRect).
  function uniaoComFolga(rects, folga) {
    const left = Math.min(...rects.map((r) => r.left)) - folga;
    const top = Math.min(...rects.map((r) => r.top)) - folga;
    const right = Math.max(...rects.map((r) => r.right)) + folga;
    const bottom = Math.max(...rects.map((r) => r.bottom)) + folga;
    return { left, top, right, bottom, width: right - left, height: bottom - top };
  }

  function cabeNaViewport(uniao) {
    return (
      uniao.left >= 0 &&
      uniao.top >= 0 &&
      uniao.right <= window.innerWidth &&
      uniao.bottom <= window.innerHeight
    );
  }

  // Recorta a região `uniao` (coordenadas CSS/viewport) do print de tela
  // inteira. O print vem em pixels FÍSICOS — multiplicar por devicePixelRatio
  // é obrigatório, senão em tela HiDPI o recorte sai deslocado. Se o PNG
  // resultante passar do limite, reduz a escala e recodifica até caber.
  async function recortarImagem(dataUrl, uniao) {
    const img = new Image();
    await new Promise((resolve, reject) => {
      img.onload = resolve;
      img.onerror = () => reject(new Error("falha ao carregar o print da tela"));
      img.src = dataUrl;
    });
    const dpr = window.devicePixelRatio || 1;
    const sx = Math.max(0, Math.round(uniao.left * dpr));
    const sy = Math.max(0, Math.round(uniao.top * dpr));
    const sw = Math.min(img.width - sx, Math.round(uniao.width * dpr));
    const sh = Math.min(img.height - sy, Math.round(uniao.height * dpr));
    if (sw <= 0 || sh <= 0) throw new Error("região de recorte inválida");

    let escala = 1;
    for (let tentativa = 0; tentativa < 6; tentativa++) {
      const canvas = document.createElement("canvas");
      canvas.width = Math.max(1, Math.round(sw * escala));
      canvas.height = Math.max(1, Math.round(sh * escala));
      const ctx = canvas.getContext("2d");
      ctx.drawImage(img, sx, sy, sw, sh, 0, 0, canvas.width, canvas.height);
      const dataUrlRecorte = canvas.toDataURL("image/png");
      const base64 = dataUrlRecorte.slice(dataUrlRecorte.indexOf(",") + 1);
      const bytes = Math.ceil((base64.length * 3) / 4);
      if (bytes <= LIMITE_BYTES_IMAGEM || escala <= 0.2) return base64;
      escala *= 0.7;
    }
    throw new Error("não consegui reduzir a imagem o suficiente");
  }

  browser.runtime.onMessage.addListener(async (msg) => {
    if (msg?.type !== "start") return;
    const sel = window.getSelection();
    const raw = sel ? sel.toString().trim() : "";
    if (!raw) return; // sem seleção não chama a API
    const range = sel.getRangeAt(0);
    const rect = range.getBoundingClientRect();
    const card = abrir(rect);

    // Coração da feature: só manda print quando a seleção encosta em algo
    // visual. Sem esse filtro toda questão custaria 5-8s de visão em vez dos
    // 150ms de hoje, e a maioria das questões é puro texto.
    const visuais = encontrarElementosVisuais(range);
    let imageBase64 = null;
    let imageMime = null;

    if (visuais.length) {
      const uniao = uniaoComFolga([rect, ...visuais.map((elemento) => elemento.getBoundingClientRect())], FOLGA_RECORTE_PX);
      if (!cabeNaViewport(uniao)) {
        // captureVisibleTab só fotografa o que está na viewport. Mandar um
        // recorte cortado seria pior que não responder — o modelo veria uma
        // figura incompleta. Melhor parar aqui e não gastar a chamada.
        mostrarErro(card, "A figura da questão não cabe inteira na tela. Role até ela ficar totalmente visível e tente de novo.", rect);
        return;
      }
      card.textContent = "Capturando a tela…";
      posicionar(card, rect);
      const printResp = await browser.runtime.sendMessage({ type: "print" });
      if (!document.getElementById(ID)) return; // usuário fechou enquanto carregava
      if (!printResp?.ok) {
        mostrarErro(card, printResp?.error || "Não consegui capturar a tela.", rect);
        return;
      }
      card.textContent = "Recortando a imagem…";
      posicionar(card, rect);
      try {
        imageBase64 = await recortarImagem(printResp.dataUrl, uniao);
        imageMime = "image/png";
      } catch (e) {
        mostrarErro(card, "Não consegui recortar a imagem da questão.", rect);
        return;
      }
    }

    card.textContent = imageBase64 ? "Analisando a imagem…" : "Consultando…";
    posicionar(card, rect);
    const resp = await browser.runtime.sendMessage({ type: "ask", raw, imageBase64, imageMime });
    if (!document.getElementById(ID)) return; // usuário fechou enquanto carregava
    if (resp?.ok) mostrarResposta(card, resp.data, rect, !!imageBase64);
    else mostrarErro(card, resp?.error, rect);
  });
}
