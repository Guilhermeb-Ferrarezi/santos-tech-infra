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
