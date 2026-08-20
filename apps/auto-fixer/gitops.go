package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// safeRef valida branches/paths que vão para o argv do git. Bloqueia
// argument-injection: valores começando com "-" viram flags (ex.:
// --upload-pack=cmd → execução arbitrária).
var safeRef = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func validRef(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && safeRef.MatchString(s)
}

func gitEnvAskpass(token string) ([]string, func(), error) {
	f, err := os.CreateTemp("", "askpass-*.sh")
	if err != nil {
		return nil, func() {}, err
	}
	// printf com aspas simples escapadas: o token nunca é interpretado pelo
	// shell (evita command-injection se ele contiver `"`, `$`, backtick etc).
	esc := strings.ReplaceAll(token, "'", "'\\''")
	script := "#!/bin/sh\nprintf '%s\\n' '" + esc + "'\n"
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, func() {}, err
	}
	f.Close()
	os.Chmod(f.Name(), 0o700)
	cleanup := func() { os.Remove(f.Name()) }
	env := append(os.Environ(),
		"GIT_ASKPASS="+f.Name(),
		"GIT_TERMINAL_PROMPT=0",
	)
	return env, cleanup, nil
}

func cloneRepo(ctx context.Context, repoURL, branch, dest, token string) error {
	env, cleanup, err := gitEnvAskpass(token)
	if err != nil {
		return err
	}
	defer cleanup()
	// Valida tudo que vai para o argv (argument-injection): só https, branch/dest
	// sem hífen inicial. Como o webhook/Coolify alimentam esses valores, não confiamos.
	if !strings.HasPrefix(repoURL, "https://") {
		return fmt.Errorf("git clone: url deve ser https: %q", repoURL)
	}
	if !validRef(branch) {
		return fmt.Errorf("git clone: branch inválida: %q", branch)
	}
	if strings.HasPrefix(dest, "-") {
		return fmt.Errorf("git clone: dest inválido: %q", dest)
	}
	// Insere o usuário no URL pra o askpass fornecer a senha (token).
	url := repoURL
	if !strings.Contains(url, "@") {
		url = "https://git@" + strings.TrimPrefix(url, "https://")
	}
	// `--` encerra as flags: url/dest nunca são interpretados como opções.
	args := []string{"clone", "--depth", "50", "--branch", branch, "--", url, dest}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, out)
	}
	return nil
}

// Tetos do fix automático (#6). O commit vai direto para a branch de deploy, sem
// review humano: um patch grande não é "correção do build quebrado", é mudança de
// projeto — melhor abortar e deixar para uma pessoa.
const (
	maxFixFiles = 20
	maxFixLines = 800
)

// forbiddenFixPath marca os caminhos fora do alcance do auto-fix. Alterá-los muda
// COMO o código é construído e implantado (ou a própria infra), não o bug que
// quebrou o build — e o push é direto na branch de deploy, com o token da org.
func forbiddenFixPath(p string) bool {
	p = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "./")
	if p == "" {
		return true
	}
	// Pipelines de CI/CD (em qualquer nível) e o próprio .git.
	if p == ".github/workflows" || strings.HasPrefix(p, ".github/workflows/") ||
		strings.Contains(p, "/.github/workflows/") || strings.HasPrefix(p, ".git/") {
		return true
	}
	// Diretórios de infraestrutura (raiz ou aninhados).
	if p == "infra" || strings.HasPrefix(p, "infra/") || strings.Contains(p, "/infra/") {
		return true
	}
	// Qualquer Dockerfile (inclusive variantes Dockerfile.prod).
	base := path.Base(p)
	return base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile.")
}

// parseNumstatZ lê `git diff --numstat --no-renames -z`: registros
// "adicionadas <TAB> removidas <TAB> caminho" separados por NUL. Arquivos binários vêm com
// "-" nas contagens e não somam linhas.
func parseNumstatZ(out string) (files []string, lines int, err error) {
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			return nil, 0, fmt.Errorf("git numstat inesperado: %q", rec)
		}
		files = append(files, parts[2])
		for _, n := range parts[:2] {
			if n == "-" {
				continue // binário
			}
			v, cerr := strconv.Atoi(n)
			if cerr != nil {
				return nil, 0, fmt.Errorf("git numstat inesperado: %q", rec)
			}
			lines += v
		}
	}
	return files, lines, nil
}

// commitAll estagia as mudanças do Claude e commita — mas só depois de auditar o
// que foi estagiado. O `git add -A` cego de antes commitava e empurrava para a
// branch de deploy qualquer coisa que o Claude tivesse tocado, incluindo workflows
// de CI, Dockerfiles e infra/, e sem nenhum teto de tamanho.
func commitAll(ctx context.Context, dir, msg string) (bool, error) {
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// runOut usa só o stdout: o CombinedOutput misturaria stderr no numstat.
	runOut := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		return string(out), err
	}
	if out, err := run("add", "-A"); err != nil {
		return false, fmt.Errorf("git add: %w: %s", err, out)
	}
	// Aborta desfazendo o staging, para não deixar o workdir armado.
	abort := func(format string, a ...any) (bool, error) {
		_, _ = run("reset")
		return false, fmt.Errorf(format, a...)
	}

	// --no-renames evita o formato de par (origem/destino) do numstat; -z evita o
	// quoting de caminhos com espaço/acento.
	st, err := runOut("diff", "--cached", "--numstat", "--no-renames", "-z")
	if err != nil {
		return abort("git diff --cached: %w", err)
	}
	files, lines, err := parseNumstatZ(st)
	if err != nil {
		return abort("%w", err)
	}
	if len(files) == 0 {
		return false, nil // nada mudou
	}

	var blocked []string
	for _, f := range files {
		if forbiddenFixPath(f) {
			blocked = append(blocked, f)
		}
	}
	if len(blocked) > 0 {
		return abort("fix bloqueado: caminhos protegidos alterados (%s)", strings.Join(blocked, ", "))
	}
	if len(files) > maxFixFiles {
		return abort("fix bloqueado: %d arquivos alterados, teto é %d", len(files), maxFixFiles)
	}
	if lines > maxFixLines {
		return abort("fix bloqueado: %d linhas alteradas, teto é %d", lines, maxFixLines)
	}

	_, _ = run("config", "user.email", "auto-fixer@santos-tech.com")
	_, _ = run("config", "user.name", "auto-fixer")
	if out, err := run("commit", "-m", msg); err != nil {
		return false, fmt.Errorf("git commit: %w: %s", err, out)
	}
	return true, nil
}

func pushBranch(ctx context.Context, dir, branch, token string) error {
	env, cleanup, err := gitEnvAskpass(token)
	if err != nil {
		return err
	}
	defer cleanup()
	if !validRef(branch) {
		return fmt.Errorf("git push: branch inválida: %q", branch)
	}
	// `--` encerra as flags antes do refspec.
	cmd := exec.CommandContext(ctx, "git", "push", "origin", "--", "HEAD:"+branch)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w: %s", err, out)
	}
	return nil
}

func headSHA(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// safeSegment valida os pedaços do nome do diretório de trabalho que vêm de fora
// (nome do app no payload da Coolify, id do incidente).
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// workdirFor monta o diretório de trabalho do incidente dentro do WorkspaceRoot.
//
// SEGURANÇA (#7): `app` vinha cru do payload do webhook direto para o
// filepath.Join, que NORMALIZA "..". Um app chamado "../../etc" fazia o clone —
// e o os.RemoveAll da limpeza — escapar do WorkspaceRoot. Agora cada segmento é
// validado e o caminho final precisa continuar sob a raiz.
func workdirFor(root, app, id string) (string, error) {
	if app == "" {
		app = "app" // a Coolify nem sempre manda o nome; ausência não é hostil
	}
	// Também recusa o hífen inicial: o caminho vai para o argv do git e um
	// segmento em "-" é a mesma classe de problema que validRef já cobre.
	ok := func(seg string) bool {
		return seg != "." && seg != ".." && !strings.HasPrefix(seg, "-") && safeSegment.MatchString(seg)
	}
	if !ok(app) {
		return "", fmt.Errorf("nome de app inválido para workdir: %q", app)
	}
	if !ok(id) {
		return "", fmt.Errorf("id de incidente inválido para workdir: %q", id)
	}
	root = filepath.Clean(root)
	dest := filepath.Clean(filepath.Join(root, app+"-"+id))
	if dest == root || !strings.HasPrefix(dest, root+string(filepath.Separator)) {
		return "", fmt.Errorf("workdir fora do WorkspaceRoot: %q", dest)
	}
	return dest, nil
}
