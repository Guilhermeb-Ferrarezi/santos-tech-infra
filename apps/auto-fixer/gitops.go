package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

func commitAll(ctx context.Context, dir, msg string) (bool, error) {
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if _, err := run("add", "-A"); err != nil {
		return false, err
	}
	// status --porcelain vazio => nada mudou.
	st, err := run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(st) == "" {
		return false, nil
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

func workdirFor(root, app, id string) string { return filepath.Join(root, app+"-"+id) }
