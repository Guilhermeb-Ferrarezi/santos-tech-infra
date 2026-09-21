package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const designScreenSlug = "index"
const previewTokenTTL = 30 * time.Minute

// designScreenRel é o caminho (relativo ao workdir) da única tela da fatia 1.
func designScreenRel() string { return filepath.Join("telas", designScreenSlug+".html") }

// previewSig é o HMAC que amarra o token a uma conversa e a um vencimento.
func previewSig(secret, convID string, exp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "design-preview|%s|%d", convID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// previewToken emite um token curto para o iframe de preview. Não é JWT de propósito:
// ele viaja na URL do iframe, então carrega o mínimo (vencimento + assinatura) e não
// serve para nenhuma outra rota.
func previewToken(secret, convID string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	return fmt.Sprintf("%d.%s", exp, previewSig(secret, convID, exp))
}

func verifyPreviewToken(secret, convID, token string) error {
	invalid := appErr(http.StatusNotFound, "NOT_FOUND", "Não encontrado")
	expRaw, sig, ok := strings.Cut(token, ".")
	if !ok {
		return invalid
	}
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil {
		return invalid
	}
	want := previewSig(secret, convID, exp)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return invalid
	}
	if time.Now().Unix() > exp {
		return invalid
	}
	return nil
}

// previewTypes é a whitelist de extensões servíveis pelo preview, com o
// Content-Type correspondente. O que não está aqui não existe para o preview —
// inclusive CLAUDE.md, design.json e qualquer coisa dentro de .git.
var previewTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".webp":  "image/webp",
	".gif":   "image/gif",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

// safeDesignPath resolve um caminho pedido pelo preview dentro do workdir. Recusa
// tudo que escape do diretório (inclusive via symlink) e tudo fora da whitelist.
func safeDesignPath(workdir, rel string) (string, error) {
	notFound := appErr(http.StatusNotFound, "NOT_FOUND", "Não encontrado")

	clean := filepath.Clean("/" + strings.TrimPrefix(rel, "/"))
	if _, ok := previewTypes[strings.ToLower(filepath.Ext(clean))]; !ok {
		return "", notFound
	}
	full := filepath.Join(workdir, clean)

	// Resolve symlinks dos dois lados antes de comparar: sem isso um link dentro
	// do workdir serviria qualquer arquivo do container.
	realRoot, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return "", notFound
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", notFound
	}
	if realFull != realRoot && !strings.HasPrefix(realFull, realRoot+string(os.PathSeparator)) {
		return "", notFound
	}
	info, err := os.Stat(realFull)
	if err != nil || info.IsDir() {
		return "", notFound
	}
	return realFull, nil
}

// designCSP é a política do CONTEÚDO do preview (não do painel). Fecha tudo e abre
// só o necessário para um mockup: estilo e script inline, Tailwind por CDN e fontes
// do Google. connect-src 'none' e img-src sem https fecham os dois canais baratos de
// exfiltração (fetch e URL de imagem) — o HTML aqui é gerado por um modelo e tratado
// como não confiável.
func designCSP(cfg Config) string {
	ancestors := "'self'"
	if len(cfg.CORSOrigins) > 0 {
		ancestors = strings.Join(cfg.CORSOrigins, " ")
	}
	return strings.Join([]string{
		"default-src 'none'",
		"img-src 'self' data:",
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		"font-src 'self' data: https://fonts.gstatic.com",
		"script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdn.tailwindcss.com",
		"connect-src 'none'",
		"form-action 'none'",
		"base-uri 'none'",
		"frame-ancestors " + ancestors,
	}, "; ")
}

// inspectorScript roda DENTRO do preview quando ?inspect=1. Marca o elemento sob o
// mouse e, no clique, manda para o painel um seletor estável e um trecho do HTML.
// Vive aqui (servidor) e não no HTML gerado: o agente não precisa saber que existe.
const inspectorScript = `<script id="santos-design-inspect">
(function () {
  var alvo = null;
  var estilo = document.createElement("style");
  estilo.textContent = "[data-stx-hover]{outline:2px solid #6366f1 !important;outline-offset:2px !important;cursor:crosshair !important}";
  document.head.appendChild(estilo);

  function seletor(el) {
    var partes = [];
    while (el && el.nodeType === 1 && partes.length < 5 && el !== document.body) {
      var p = el.tagName.toLowerCase();
      if (el.id) { partes.unshift(p + "#" + el.id); break; }
      var cls = (el.getAttribute("class") || "").trim().split(/\s+/).filter(Boolean).slice(0, 2);
      if (cls.length) p += "." + cls.join(".");
      var pai = el.parentElement;
      if (pai) {
        var irmaos = Array.prototype.filter.call(pai.children, function (c) { return c.tagName === el.tagName; });
        if (irmaos.length > 1) p += ":nth-of-type(" + (irmaos.indexOf(el) + 1) + ")";
      }
      partes.unshift(p);
      el = pai;
    }
    return partes.join(" > ");
  }

  document.addEventListener("mouseover", function (e) {
    if (alvo) alvo.removeAttribute("data-stx-hover");
    alvo = e.target;
    if (alvo && alvo.setAttribute) alvo.setAttribute("data-stx-hover", "1");
  }, true);

  document.addEventListener("click", function (e) {
    e.preventDefault();
    e.stopPropagation();
    var el = e.target;
    if (!el || !el.tagName) return;
    var html = (el.outerHTML || "").slice(0, 600);
    parent.postMessage({ source: "santos-design-inspect", selector: seletor(el), html: html, tag: el.tagName.toLowerCase() }, "*");
  }, true);
})();
</script>`

// injectInspector coloca o script no fim do documento — depois do conteúdo, para
// que os listeners encontrem a árvore montada.
func injectInspector(html []byte) []byte {
	s := string(html)
	if i := strings.LastIndex(strings.ToLower(s), "</body>"); i >= 0 {
		return []byte(s[:i] + inspectorScript + s[i:])
	}
	return []byte(s + inspectorScript)
}

// designGuide é o CLAUDE.md do workspace de design. É ele que dá coerência visual
// entre gerações e informa as restrições do preview (origem opaca, CSP fechada).
const designGuide = `# Projeto de design

Você está desenhando interfaces. O resultado é servido num preview isolado e
aparece ao lado da conversa. Edite os arquivos deste diretório — não responda com
o HTML no chat.

## Regras do arquivo

- A tela vive em ` + "`telas/index.html`" + `. Edite esse arquivo; não crie outros
  sem o usuário pedir.
- HTML autocontido: estilos no próprio arquivo (` + "`<style>`" + ` ou classes do
  Tailwind), sem build.
- Tailwind está disponível por ` + "`<script src=\"https://cdn.tailwindcss.com\"></script>`" + `.
  Fontes do Google Fonts são permitidas.
- **Nenhuma requisição de dados.** ` + "`fetch`" + `, XHR e WebSocket estão bloqueados
  pela política do preview. Dados de exemplo ficam embutidos no HTML.
- **Nenhuma imagem externa.** Use SVG inline, gradientes ou blocos de cor. URLs de
  imagem de fora não carregam.

## Como trabalhar

- Mudança pedida = edição cirúrgica no trecho pertinente. Não reescreva a página
  inteira quando pedirem para mudar um botão.
- Quando o pedido vier com um elemento selecionado (aparece como "elemento
  selecionado" no pedido), mexa nesse elemento e no que for indispensável.
- Mantenha a linguagem visual entre pedidos: a mesma paleta, a mesma escala de
  espaçamento, a mesma família tipográfica. Mudança de identidade só quando pedida.
- Escreva a interface em português do Brasil, com acentuação correta.
`

const designStarterHTML = `<!doctype html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Novo design</title>
<script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="min-h-screen bg-neutral-50 text-neutral-900 antialiased">
  <main class="mx-auto flex min-h-screen max-w-xl flex-col items-center justify-center gap-3 px-6 text-center">
    <h1 class="text-2xl font-semibold tracking-tight">Projeto em branco</h1>
    <p class="text-sm text-neutral-500">Descreva a interface que você quer e ela aparece aqui.</p>
  </main>
</body>
</html>
`

// designGitignore impede que segredos que não deveriam existir aqui — mas podem
// aparecer por um bug futuro ou caminho não previsto — acabem versionados no git
// local do workspace. .mcp.json carrega o PAT do GitHub em texto puro (mcp.go);
// hoje kind=design nunca deveria ter esse arquivo (bloqueado na criação em
// handlers_conv.go), mas isso é defesa em profundidade, não a única barreira.
const designGitignore = ".mcp.json\n"

type designManifest struct {
	Title     string    `json:"title"`
	Screen    string    `json:"screen"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// gitRun roda um comando git dentro de dir e devolve a saída aparada. Erro traz a
// saída combinada, que é onde o git explica o motivo.
func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Claude Design", "GIT_AUTHOR_EMAIL=design@santos-tech.com",
		"GIT_COMMITTER_NAME=Claude Design", "GIT_COMMITTER_EMAIL=design@santos-tech.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// inspectPrefixo é a primeira linha que buildPromptWithTarget (web/src/lib/design/
// inspect.ts, repo dashboard) coloca no prompt quando o usuário aponta um elemento
// no preview.
//
// CONTRATO SEM SCHEMA COMPARTILHADO: os dois lados (TS gera, Go interpreta) mantêm
// esse formato em sincronia só por convenção — não há teste que rode os dois juntos.
// Mudar este texto, a cerca de código ou a ordem das linhas SEM mudar o outro lado
// quebra silenciosamente a extração do assunto do commit (volta a usar o seletor CSS
// como assunto, o bug do achado original). Ao mexer aqui, atualize também:
// buildPromptWithTarget e seu teste de formato exato (inspect.test.ts, repo
// dashboard), e o fixture `comAlvo`/`multi` de TestResumoDoPrompt logo abaixo.
const inspectPrefixo = "Elemento selecionado no preview: "

// pedidoDepoisDoContexto extrai o pedido real de um prompt prefixado pelo inspetor.
// O formato produzido pelo front é:
//
//	Elemento selecionado no preview: `<seletor>`
//	```html
//	<html do elemento>
//	```
//
//	<pedido do usuário>
//
// Sem isso o assunto de todo commit feito via "inspecionar" seria o seletor, e o
// histórico — a razão de existir do commit por turno — não diria o que mudou.
// Qualquer desvio do formato (bloco não fechado, nada depois dele) devolve false
// para o chamador cair no comportamento antigo: primeira linha, sem erro.
func pedidoDepoisDoContexto(prompt string) (string, bool) {
	if !strings.HasPrefix(prompt, inspectPrefixo) {
		return "", false
	}
	linhas := strings.Split(prompt, "\n")
	fim := -1
	for i := 1; i < len(linhas); i++ {
		if strings.TrimSpace(linhas[i]) == "```" {
			fim = i
			break
		}
	}
	if fim < 0 {
		return "", false
	}
	for _, l := range linhas[fim+1:] {
		if t := strings.TrimSpace(l); t != "" {
			return t, true
		}
	}
	return "", false
}

// resumoDoPrompt transforma o pedido do usuário no assunto do commit: uma linha,
// no máximo 72 caracteres.
//
// Quando o prompt vem do inspetor ("Elemento selecionado no preview: ..."), o
// assunto sai do pedido de verdade, não do seletor.
func resumoDoPrompt(prompt string) string {
	linha := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if pedido, ok := pedidoDepoisDoContexto(prompt); ok {
		linha = pedido
	}
	if linha == "" {
		linha = "ajuste no design"
	}
	if len([]rune(linha)) > 72 {
		linha = string([]rune(linha)[:69]) + "..."
	}
	return linha
}

// commitDesignTurn registra no git o estado do workdir ao fim de um turno. Devolve
// o sha curto, ou "" quando o turno não mexeu em arquivo nenhum.
//
// Quem commita é a API, não o agente: pedir ao modelo que lembre de commitar é
// combinar com quem esquece, e sem histórico não há como recuperar um design que
// o turno seguinte estragou.
func (s *Server) commitDesignTurn(conv *Conversation, prompt string) (string, error) {
	if _, err := gitRun(conv.Workdir, "add", "-A"); err != nil {
		return "", err
	}
	// --quiet + exit code: 0 = sem diferença, 1 = há o que commitar.
	if _, err := gitRun(conv.Workdir, "diff", "--cached", "--quiet"); err == nil {
		return "", nil
	}
	if _, err := gitRun(conv.Workdir, "commit", "-m", resumoDoPrompt(prompt)); err != nil {
		return "", err
	}
	return gitRun(conv.Workdir, "rev-parse", "--short", "HEAD")
}

// designTurnEvent decide qual evento de WebSocket despachar depois de
// commitDesignTurn rodar, a partir do resultado dela. Separada de onTurnEnd
// pra ser testável sem o harness de processo do liveSession. Erro já foi
// logado pelo chamador — aqui só decide se ele impede o despacho de evento.
func designTurnEvent(sha string, err error) *turnEvent {
	if err != nil {
		return nil
	}
	if sha == "" {
		return &turnEvent{Type: "design_no_change"}
	}
	return &turnEvent{Type: "design_updated", Text: sha}
}

// bootstrapDesignWorkspace prepara o workdir de um projeto de design: guia, tela
// inicial, manifesto e repositório git com o commit de origem. Idempotente — nunca
// sobrescreve arquivo que já existe (pode rodar de novo numa conversa antiga).
func (s *Server) bootstrapDesignWorkspace(conv *Conversation) error {
	if err := os.MkdirAll(filepath.Join(conv.Workdir, "telas"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(conv.Workdir, "assets"), 0o755); err != nil {
		return err
	}

	writeIfAbsent := func(rel, content string) error {
		p := filepath.Join(conv.Workdir, rel)
		if _, err := os.Stat(p); err == nil {
			return nil
		}
		return os.WriteFile(p, []byte(content), 0o644)
	}

	if err := writeIfAbsent("CLAUDE.md", designGuide); err != nil {
		return err
	}
	if err := writeIfAbsent(designScreenRel(), designStarterHTML); err != nil {
		return err
	}
	if err := writeIfAbsent(".gitignore", designGitignore); err != nil {
		return err
	}

	title := "Novo design"
	if conv.Title != nil && *conv.Title != "" {
		title = *conv.Title
	}
	man, err := json.MarshalIndent(designManifest{
		Title:     title,
		Screen:    filepath.ToSlash(designScreenRel()),
		UpdatedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeIfAbsent("design.json", string(man)+"\n"); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(conv.Workdir, ".git")); err != nil {
		if _, err := gitRun(conv.Workdir, "init", "--initial-branch=main"); err != nil {
			return err
		}
	}
	if _, err := gitRun(conv.Workdir, "add", "-A"); err != nil {
		return err
	}
	// Sem mudança, o commit falha com "nothing to commit" — e nesse caso o
	// histórico já existe, então não é erro.
	if out, err := gitRun(conv.Workdir, "commit", "-m", "chore: projeto de design criado"); err != nil {
		if !strings.Contains(err.Error(), "nothing to commit") {
			return err
		}
		_ = out
	}
	return nil
}
