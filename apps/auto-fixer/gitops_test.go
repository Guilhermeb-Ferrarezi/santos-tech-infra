package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidRef(t *testing.T) {
	ok := []string{"main", "master", "feat/x", "release-1.2.3", "a_b.c"}
	for _, s := range ok {
		if !validRef(s) {
			t.Errorf("validRef(%q) deveria ser true", s)
		}
	}
	bad := []string{"", "-rf", "--upload-pack=evil", "foo bar", "a;b", "$(x)", "a\nb"}
	for _, s := range bad {
		if validRef(s) {
			t.Errorf("validRef(%q) deveria ser false", s)
		}
	}
}

func TestForbiddenFixPath(t *testing.T) {
	blocked := []string{
		".github/workflows/deploy.yml",
		"apps/api/.github/workflows/ci.yml",
		"Dockerfile",
		"apps/api/Dockerfile",
		"apps/api/Dockerfile.prod",
		"infra",
		"infra/terraform/main.tf",
		"apps/api/infra/compose.yml",
		".git/config",
		"",
	}
	for _, p := range blocked {
		if !forbiddenFixPath(p) {
			t.Errorf("forbiddenFixPath(%q) deveria bloquear", p)
		}
	}
	allowed := []string{
		"main.go",
		"apps/api/handler.go",
		"README.md",
		"docs/infraestrutura.md",
		"pkg/dockerfiles.go",
		".github/dependabot.yml",
	}
	for _, p := range allowed {
		if forbiddenFixPath(p) {
			t.Errorf("forbiddenFixPath(%q) deveria permitir", p)
		}
	}
}

func TestParseNumstatZ(t *testing.T) {
	in := "3\t1\tmain.go\x000\t7\tdocs/a.md\x00-\t-\tlogo.png\x00"
	files, lines, err := parseNumstatZ(in)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(files) != 3 || files[0] != "main.go" || files[2] != "logo.png" {
		t.Errorf("files = %v", files)
	}
	if lines != 11 { // binário (-/-) não soma
		t.Errorf("lines = %d, esperado 11", lines)
	}
	if _, _, err := parseNumstatZ("lixo\x00"); err == nil {
		t.Error("registro malformado deveria dar erro")
	}
}

// gitRepo monta um repo temporário com um commit inicial.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git indisponível")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	write(t, dir, "main.go", "package main\n")
	run("add", "-A")
	run("commit", "-qm", "base")
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCommitAllBlocksProtectedPaths(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "Dockerfile", "FROM scratch\n")
	changed, err := commitAll(context.Background(), dir, "fix(auto): x")
	if err == nil || changed {
		t.Fatalf("deveria bloquear o Dockerfile, got changed=%v err=%v", changed, err)
	}
	if !strings.Contains(err.Error(), "caminhos protegidos") {
		t.Errorf("mensagem inesperada: %v", err)
	}
}

func TestCommitAllBlocksTooManyFiles(t *testing.T) {
	dir := gitRepo(t)
	for i := 0; i < maxFixFiles+1; i++ {
		write(t, dir, fmt.Sprintf("f%d.go", i), "package main\n")
	}
	if changed, err := commitAll(context.Background(), dir, "fix(auto): x"); err == nil || changed {
		t.Fatalf("deveria bloquear por número de arquivos, got changed=%v err=%v", changed, err)
	}
}

func TestCommitAllBlocksTooManyLines(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "big.go", strings.Repeat("// linha\n", maxFixLines+1))
	if changed, err := commitAll(context.Background(), dir, "fix(auto): x"); err == nil || changed {
		t.Fatalf("deveria bloquear por número de linhas, got changed=%v err=%v", changed, err)
	}
}

func TestCommitAllAcceptsSmallFix(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	changed, err := commitAll(context.Background(), dir, "fix(auto): ok")
	if err != nil || !changed {
		t.Fatalf("fix pequeno deveria passar, got changed=%v err=%v", changed, err)
	}
}

func TestCommitAllNoChanges(t *testing.T) {
	dir := gitRepo(t)
	if changed, err := commitAll(context.Background(), dir, "fix(auto): nada"); err != nil || changed {
		t.Fatalf("sem mudanças deveria ser (false, nil), got changed=%v err=%v", changed, err)
	}
}

func TestWorkdirForRejeitaTraversal(t *testing.T) {
	root := filepath.Join(string(filepath.Separator)+"data", "workspaces")
	bad := []string{"../../etc", "..", ".", "a/b", "a" + string(filepath.Separator) + "b", "app;rm", "app name", "-rf"}
	for _, app := range bad {
		if got, err := workdirFor(root, app, "id1"); err == nil {
			t.Errorf("workdirFor(app=%q) deveria falhar, devolveu %q", app, got)
		}
	}
	if _, err := workdirFor(root, "bot-go", "../../etc"); err == nil {
		t.Error("id com traversal deveria falhar")
	}
	got, err := workdirFor(root, "bot-go", "abc-123")
	if err != nil {
		t.Fatalf("caso válido falhou: %v", err)
	}
	if want := filepath.Join(root, "bot-go-abc-123"); got != want {
		t.Errorf("workdirFor = %q, want %q", got, want)
	}
	// App vazio (a Coolify nem sempre manda o nome) continua funcionando.
	if got, err := workdirFor(root, "", "abc"); err != nil || !strings.HasPrefix(got, root) {
		t.Errorf("app vazio: got %q err %v", got, err)
	}
}
