package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A tabela users de PRODUÇÃO não tem updated_at (nasceu no Drizzle; ver as
// colunas no banco). O db/schema.sql declara a coluna, e por isso um teste de
// integração montado a partir dele passa com uma query que em produção dá erro.
// Aconteceu em 25/09/2026: PATCH /auth/me/avisos respondia 500 e o "Salvar
// avisos" da tela não gravava nada.
func TestNenhumUpdateDeUsersEscreveUpdatedAt(t *testing.T) {
	arquivos, err := filepath.Glob("db/query/*.sql")
	if err != nil || len(arquivos) == 0 {
		t.Fatalf("sem queries: %v", err)
	}
	update := regexp.MustCompile(`(?is)UPDATE\s+users\s+SET(.*?);`)
	for _, a := range arquivos {
		b, err := os.ReadFile(a)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range update.FindAllStringSubmatch(string(b), -1) {
			if strings.Contains(strings.ToLower(m[1]), "updated_at") {
				t.Errorf("%s: UPDATE users escreve updated_at, que não existe em produção:\n%s", a, m[0])
			}
		}
	}
}
