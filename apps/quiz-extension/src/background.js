// Background da extensão: guarda a sessão, fala com a api-go e dispara a
// captura de conteúdo quando o atalho é pressionado. Também é quem tira o
// print da aba (tabs.captureVisibleTab) a pedido do content script, que não
// tem acesso a essa API. Nenhuma credencial de API passa por aqui — a
// extensão só conhece o próprio login do usuário.

// Shim mínimo: Firefox expõe `browser.*` (promises nativas), Chrome expõe só
// `chrome.*` (que também aceita promise quando o callback é omitido, desde
// Chrome MV3). Sem isso o arquivo inteiro só funcionaria num dos dois.
const api = globalThis.browser ?? globalThis.chrome;

const API = "https://api.santos-tech.com";

async function getTokens() {
  const { tokens } = await api.storage.local.get("tokens");
  return tokens || null;
}

async function setTokens(tokens) {
  await api.storage.local.set({ tokens });
}

// Fila de refresh: o refresh token é rotativo e fail-closed — reusar um token
// já rotacionado faz o servidor revogar TODAS as sessões do usuário. Dois
// refresh concorrentes causariam exatamente isso, então só existe um em voo.
let refreshInFlight = null;

async function refreshTokens() {
  if (refreshInFlight) return refreshInFlight;
  refreshInFlight = (async () => {
    const tokens = await getTokens();
    if (!tokens?.refreshToken) throw new Error("sem sessão");
    const res = await fetch(`${API}/auth/refresh`, {
      method: "POST",
      headers: { Authorization: `Bearer ${tokens.refreshToken}` },
    });
    if (!res.ok) {
      await api.storage.local.remove("tokens");
      throw new Error("sessão expirada");
    }
    const data = await res.json();
    // Gravar o par novo ANTES de qualquer outra coisa: se o processo morrer
    // aqui, o token antigo já não vale e o novo se perdeu.
    await setTokens({ accessToken: data.accessToken, refreshToken: data.refreshToken });
    return data.accessToken;
  })();
  try {
    return await refreshInFlight;
  } finally {
    refreshInFlight = null;
  }
}

// Sem AbortController/timeout próprio aqui: o fetch() do navegador não tem
// timeout implícito, então o orçamento de até 50s do servidor no caminho com
// imagem já é acomodado sem precisar de ajuste — só espera a resposta chegar.
async function apiFetch(path, body, { retried = false } = {}) {
  const tokens = await getTokens();
  if (!tokens?.accessToken) throw new Error("Faça login nas opções da extensão");
  const res = await fetch(`${API}${path}`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${tokens.accessToken}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
  if (res.status === 401 && !retried) {
    await refreshTokens();
    return apiFetch(path, body, { retried: true });
  }
  const data = await res.json().catch(() => ({}));
  // writeErr serializa {"code","message"} no topo (errors.go:32) — não aninhado.
  if (!res.ok) throw new Error(data?.message || `erro ${res.status}`);
  return data;
}

async function login(identifier, password) {
  const res = await fetch(`${API}/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ identifier, password }),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data?.message || `erro ${res.status}`);
  if (data.mfaRequired) {
    throw new Error("Sua conta pede 2FA e a extensão ainda não trata isso.");
  }
  if (!data.accessToken) {
    // Acontece se o servidor não reconhecer a origem da extensão (moz-extension://
    // ou chrome-extension://) como cliente nativo (ver isNativeClient no
    // api-go): login funciona, tokens não vêm, e a extensão ficaria logada e
    // inútil. Sintoma exato de o servidor ainda não ter o ajuste pro navegador
    // em uso.
    throw new Error(
      "Login OK, mas o servidor não devolveu tokens — o servidor não reconheceu a origem da extensão (api-go precisa do ajuste em isNativeClient para este navegador)."
    );
  }
  await setTokens({ accessToken: data.accessToken, refreshToken: data.refreshToken });
}

api.commands.onCommand.addListener(async (command) => {
  if (command !== "answer-selection") return;
  const [tab] = await api.tabs.query({ active: true, currentWindow: true });
  if (!tab?.id) return;
  try {
    await api.scripting.executeScript({ target: { tabId: tab.id }, files: ["src/content.js"] });
    await api.tabs.sendMessage(tab.id, { type: "start" });
  } catch (e) {
    console.error("quiz-jev: não consegui injetar na aba", e);
  }
});

// Padrão sendResponse + `return true`: funciona em Firefox E Chrome. O padrão
// anterior (`return apiFetch(...).then(...)`, uma Promise) é só Firefox — no
// Chrome a mensagem nunca é respondida e quem espera (content.js) trava.
api.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg?.type === "ask") {
    const body = { raw: msg.raw, explain: !!msg.explain };
    // imageBase64/imageMime só vêm quando o content script detectou conteúdo
    // visual na seleção (gráfico, tabela, figura) — ver content.js. Sem
    // imagem, o corpo é idêntico ao de antes e o fluxo rápido continua igual.
    if (msg.imageBase64) {
      body.imageBase64 = msg.imageBase64;
      body.imageMime = msg.imageMime || "image/png";
    }
    apiFetch("/quiz/answer", body)
      .then((data) => sendResponse({ ok: true, data }))
      .catch((e) => sendResponse({ ok: false, error: e.message }));
    return true;
  }
  if (msg?.type === "print") {
    // O content script não pode chamar tabs.captureVisibleTab — só o
    // background tem acesso a essa API. activeTab (concedida pelo próprio
    // atalho Alt+Q) já é suficiente nos dois navegadores; não precisa de
    // permissão extra nem no Chrome.
    api.tabs
      .captureVisibleTab(null, { format: "png" })
      .then((dataUrl) => sendResponse({ ok: true, dataUrl }))
      .catch((e) => sendResponse({ ok: false, error: e.message || "falha ao capturar a tela" }));
    return true;
  }
  if (msg?.type === "login") {
    login(msg.identifier, msg.password)
      .then(() => sendResponse({ ok: true }))
      .catch((e) => sendResponse({ ok: false, error: e.message }));
    return true;
  }
  if (msg?.type === "status") {
    getTokens().then((t) => sendResponse({ loggedIn: !!t?.accessToken }));
    return true;
  }
  return false;
});
