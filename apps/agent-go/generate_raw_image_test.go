package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Estes testes provam o bug corrigido (task "raw" descartava a imagem em
// silêncio) SEM depender do CLI `claude` real: `s.cfg.ClaudeBin` aponta para
// um script `sh` fake, gerado por teste, que grava os argumentos recebidos
// (separados por \x1f, um por invocação) e o stdin (o prompt) em arquivos
// fixos, e devolve um envelope de resposta válido — json puro quando os
// argumentos não pedem stream-json, ou uma linha "result" de stream-json
// quando pedem. Isso deixa inspecionar exatamente o que generateOnce e
// generateOnceWithTrace mandaram pro CLI, incluindo se a imagem foi
// realmente gravada em disco no diretório liberado por --add-dir.

// shellQuote envolve s em aspas simples pra uso seguro dentro do script sh
// gerado (os caminhos vêm de t.TempDir(), mas não custa nada garantir).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeFakeClaudeScript grava um `claude` falso que loga argumentos (em
// argsLog, um registro ARGS\x1f<arg>\x1f<arg>... por invocação) e o conteúdo
// bruto do stdin (em stdinPath, sobrescrito a cada invocação). Se os
// argumentos contiverem "--add-dir", lista o conteúdo desse diretório em
// listPath — é como o teste confirma que o anexo foi gravado e está
// acessível no momento em que o CLI (fake) roda, antes do
// `defer os.RemoveAll(dir)` de generateOnce apagar tudo.
func writeFakeClaudeScript(t *testing.T, argsLog, stdinPath, listPath, resultText string) string {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "claude")
	script := fmt.Sprintf(`#!/bin/sh
{
  printf 'ARGS'
  for a in "$@"; do printf '\037%%s' "$a"; done
  printf '\n'
} >> %s
cat > %s

prev=""
for a in "$@"; do
  if [ "$prev" = "--add-dir" ]; then
    ls -1 "$a" > %s 2>&1 || true
  fi
  prev="$a"
done

case " $* " in
  *" stream-json "*)
    printf '%%s\n' '{"type":"result","subtype":"success","result":"%s","total_cost_usd":0.001,"usage":{"input_tokens":1,"output_tokens":1}}'
    ;;
  *)
    printf '%%s\n' '{"result":"%s","total_cost_usd":0.001,"usage":{"input_tokens":1,"output_tokens":1}}'
    ;;
esac
`, shellQuote(argsLog), shellQuote(stdinPath), shellQuote(listPath), resultText, resultText)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("escrever fake claude: %v", err)
	}
	return scriptPath
}

// splitArgsLog lê UM registro ARGS do log (falha se não houver exatamente um —
// os testes abaixo fazem uma única chamada ao endpoint cada).
func splitArgsLog(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler args log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("esperava 1 invocação do CLI, veio %d: %q", len(lines), lines)
	}
	parts := strings.Split(lines[0], "\x1f")
	if len(parts) == 0 || parts[0] != "ARGS" {
		t.Fatalf("log de args mal formado: %q", lines[0])
	}
	return parts[1:]
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// argAfter devolve o argumento seguinte a `flag`, ou "" se `flag` não aparece.
func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestHandleGenerateRawComImagemUsaGenerateOnce prova o fim do bug: task "raw"
// com imagem agora chega ao CLI com a imagem gravada em disco, --add-dir
// liberando o diretório certo e --dangerously-skip-permissions (igual ao
// caminho multimodal já comprovado das outras tasks) — e o prompt mandado é
// o brief PURO (com o prefixo de instrução do Read), nunca o texto de
// redator de email que buildGeneratePrompt gera por padrão.
func TestHandleGenerateRawComImagemUsaGenerateOnce(t *testing.T) {
	tmp := t.TempDir()
	argsLog := filepath.Join(tmp, "args.log")
	stdinFile := filepath.Join(tmp, "stdin.txt")
	listFile := filepath.Join(tmp, "list.txt")
	bin := writeFakeClaudeScript(t, argsLog, stdinFile, listFile, "ok-imagem")

	s := &Server{cfg: Config{WorkspaceRoot: t.TempDir(), ClaudeBin: bin, DefaultModel: "sonnet"}}

	brief := "Qual alternativa está correta, considerando o gráfico anexado?"
	imgB64 := base64.StdEncoding.EncodeToString([]byte("bytes-de-imagem-fake"))
	reqBody := fmt.Sprintf(`{"task":"raw","brief":%q,"imageBase64":%q,"imageMime":"image/png"}`, brief, imgB64)

	req := httptest.NewRequest(http.MethodPost, "/claude/generate", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleGenerate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; corpo: %s", rec.Code, rec.Body.String())
	}
	var res generateResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("resposta não é JSON: %v; corpo: %s", err, rec.Body.String())
	}
	if res.Text != "ok-imagem" {
		t.Fatalf("text = %q; queria o resultado do generateOnce", res.Text)
	}
	if len(res.ToolCalls) != 0 {
		t.Fatalf("toolCalls = %v; generateOnce não captura trace, deveria vir vazio", res.ToolCalls)
	}

	args := splitArgsLog(t, argsLog)
	if containsArg(args, "stream-json") {
		t.Fatalf("com imagem deveria usar generateOnce (--output-format json), não stream-json: %v", args)
	}
	if !containsArg(args, "--dangerously-skip-permissions") {
		t.Fatalf("faltou --dangerously-skip-permissions (mesma contenção do caminho multimodal existente): %v", args)
	}
	addDir := argAfter(args, "--add-dir")
	if addDir == "" {
		t.Fatalf("faltou --add-dir: %v", args)
	}

	listing, err := os.ReadFile(listFile)
	if err != nil {
		t.Fatalf("ler listagem do --add-dir: %v", err)
	}
	if !strings.Contains(string(listing), "anexo.png") {
		t.Fatalf("--add-dir (%s) não continha anexo.png no momento da chamada: %q", addDir, listing)
	}

	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("ler stdin capturado: %v", err)
	}
	prompt := string(stdin)
	if !strings.Contains(prompt, "Use a ferramenta Read") || !strings.Contains(prompt, "anexo.png") {
		t.Fatalf("prompt não instrui a ler a imagem: %q", prompt)
	}
	if !strings.Contains(prompt, brief) {
		t.Fatalf("prompt não contém o brief original: %q", prompt)
	}
	if strings.Contains(prompt, "redator de email marketing") {
		t.Fatalf("prompt passou por buildGeneratePrompt (não deveria — raw é raw): %q", prompt)
	}
}

// TestHandleGenerateRawSemImagemMantemCaminhoAntigo é o teste de regressão:
// sem imagem, task "raw" continua idêntica — generateOnceWithTrace (stream-json,
// sem --add-dir, sem --dangerously-skip-permissions) e o prompt é o brief cru.
func TestHandleGenerateRawSemImagemMantemCaminhoAntigo(t *testing.T) {
	tmp := t.TempDir()
	argsLog := filepath.Join(tmp, "args.log")
	stdinFile := filepath.Join(tmp, "stdin.txt")
	listFile := filepath.Join(tmp, "list.txt")
	bin := writeFakeClaudeScript(t, argsLog, stdinFile, listFile, "ok-texto")

	s := &Server{cfg: Config{WorkspaceRoot: t.TempDir(), ClaudeBin: bin, DefaultModel: "sonnet"}}

	brief := "Qual é a capital da França?"
	reqBody := fmt.Sprintf(`{"task":"raw","brief":%q}`, brief)

	req := httptest.NewRequest(http.MethodPost, "/claude/generate", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleGenerate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; corpo: %s", rec.Code, rec.Body.String())
	}
	var res generateResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("resposta não é JSON: %v; corpo: %s", err, rec.Body.String())
	}
	if res.Text != "ok-texto" {
		t.Fatalf("text = %q; queria o resultado do generateOnceWithTrace", res.Text)
	}

	args := splitArgsLog(t, argsLog)
	if !containsArg(args, "stream-json") {
		t.Fatalf("sem imagem deveria continuar em generateOnceWithTrace (stream-json): %v", args)
	}
	if containsArg(args, "--add-dir") {
		t.Fatalf("sem imagem não deveria haver --add-dir: %v", args)
	}
	if containsArg(args, "--dangerously-skip-permissions") {
		t.Fatalf("sem imagem não deveria haver --dangerously-skip-permissions: %v", args)
	}

	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("ler stdin capturado: %v", err)
	}
	if strings.TrimRight(string(stdin), "\n") != brief && string(stdin) != brief {
		t.Fatalf("prompt deveria ser o brief cru, veio %q", stdin)
	}
}
