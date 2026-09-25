package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santos-tech/auth/db"
)

// Cadastro com papel personalizado contra Postgres real (API_TEST_DATABASE_URL).
func TestCreateAdminUserPapelPersonalizadoIntegracao(t *testing.T) {
	url := os.Getenv("API_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("API_TEST_DATABASE_URL vazio")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: pool, q: db.New(pool), portalDB: pool}
	var cargo string
	if err := pool.QueryRow(ctx, `INSERT INTO custom_roles (name, permissions) VALUES ('Bot de vendas '||gen_random_uuid(), '{"agenda":["read"]}') RETURNING id::text`).Scan(&cargo); err != nil {
		t.Fatal(err)
	}
	cria := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		s.handleCreateAdminUser(w, httptest.NewRequest("POST", "/auth/admin/users", strings.NewReader(body)))
		var out map[string]any
		_ = json.NewDecoder(w.Body).Decode(&out)
		return w.Code, out
	}
	email := "bot-" + cargo[:8] + "@exemplo.com"
	code, out := cria(`{"email":"` + email + `","name":"Bot","role":4,"customRoleId":"` + cargo + `","password":"senha-forte-123"}`)
	u, _ := out["user"].(map[string]any)
	if code != http.StatusCreated || u["role"] != float64(4) || u["customRoleId"] != cargo {
		t.Fatalf("papel 4 com cargo: %d %v", code, out)
	}
	// Cargo inexistente: 400, e nada é criado.
	outro := "bot2-" + cargo[:8] + "@exemplo.com"
	code, out = cria(`{"email":"` + outro + `","name":"Bot","role":4,"customRoleId":"00000000-0000-0000-0000-000000000000","password":"senha-forte-123"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("cargo inexistente deveria ser 400: %d %v", code, out)
	}
	if u, _ := s.userByEmail(ctx, outro); u != nil {
		t.Fatal("cargo inexistente não pode criar a conta")
	}
	// Cargo mandado com papel que não é personalizado é ignorado.
	aluno := "aluno-" + cargo[:8] + "@exemplo.com"
	code, out = cria(`{"email":"` + aluno + `","name":"Aluno","role":1,"customRoleId":"` + cargo + `","password":"senha-forte-123"}`)
	u, _ = out["user"].(map[string]any)
	if code != http.StatusCreated || u["customRoleId"] != nil {
		t.Fatalf("aluno não leva cargo: %d %v", code, out)
	}
}
