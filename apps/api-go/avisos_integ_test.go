package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santos-tech/auth/db"
)

// Integração contra Postgres real: roda só com API_TEST_DATABASE_URL apontando
// para um banco com db/schema.sql e a migração do db.go aplicados.
func TestAvisosIntegracao(t *testing.T) {
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

	var uid int64
	email := "henrique+" + time.Now().Format("150405.000000") + "@santos-tech.com"
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, name, role) VALUES ($1, 'Henrique', 3) RETURNING id`, email).Scan(&uid); err != nil {
		t.Fatal(err)
	}

	// Captura o e-mail que sairia pela nossa API de e-mails.
	var mu sync.Mutex
	var enviados []map[string]string
	emailAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		enviados = append(enviados, m)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer emailAPI.Close()

	s := &Server{cfg: Config{EmailAPIURL: emailAPI.URL, EmailAPIKey: "x"}, q: db.New(pool)}
	s.email = newEmailClient(s.cfg)
	chama := func(h http.HandlerFunc, met, path, body string, comUsuario bool) (int, map[string]any) {
		req := httptest.NewRequest(met, path, strings.NewReader(body))
		if comUsuario {
			req = req.WithContext(context.WithValue(req.Context(), userIDKey, uid))
		}
		w := httptest.NewRecorder()
		h(w, req)
		var out map[string]any
		raw, _ := io.ReadAll(w.Body)
		_ = json.Unmarshal(raw, &out)
		return w.Code, out
	}

	code, out := chama(s.handleGetMeAvisos, "GET", "/auth/me/avisos", "", true)
	if code != 200 || out["emailLogin"] != email || out["avisoEmail"] != "" || out["avisoTelefone"] != "" {
		t.Fatalf("GET inicial = %d %v", code, out)
	}

	code, out = chama(s.handlePatchMeAvisos, "PATCH", "/auth/me/avisos", `{"avisoEmail":"Pessoal@Gmail.com","avisoTelefone":"+55 (16) 99999-0000"}`, true)
	if code != 200 || out["avisoEmail"] != "pessoal@gmail.com" || out["avisoTelefone"] != "5516999990000" {
		t.Fatalf("PATCH = %d %v", code, out)
	}
	// Campo ausente preserva.
	code, out = chama(s.handlePatchMeAvisos, "PATCH", "/auth/me/avisos", `{"avisoTelefone":"5516988887777"}`, true)
	if code != 200 || out["avisoEmail"] != "pessoal@gmail.com" || out["avisoTelefone"] != "5516988887777" {
		t.Fatalf("PATCH parcial = %d %v", code, out)
	}

	// O aviso: sino gravado, e-mail para o contato de aviso, telefone devolvido.
	code, out = chama(s.handleCreateAviso, "POST", "/avisos",
		`{"userId":`+jsonInt(uid)+`,"titulo":"Retomar Vivian","corpo":"disse: em dezembro","url":"https://santos-tech.com/dashboard/admin/whats/conversas?c=1","email":true}`, false)
	if code != 200 || out["telefone"] != "5516988887777" || out["emailEnviado"] != true {
		t.Fatalf("POST /avisos = %d %v", code, out)
	}
	var sinos int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_notifications WHERE user_id = $1 AND title = 'Retomar Vivian'`, uid).Scan(&sinos); err != nil {
		t.Fatal(err)
	}
	if sinos != 1 {
		t.Errorf("notificações no sino = %d, queria 1", sinos)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(enviados)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if len(enviados) != 1 || enviados[0]["to"] != "pessoal@gmail.com" || !strings.Contains(enviados[0]["html"], "em dezembro") {
		t.Errorf("e-mail enviado: %v", enviados)
	}
	mu.Unlock()

	// "" limpa e volta ao padrão: o e-mail vai para o de login, sem telefone.
	code, _ = chama(s.handlePatchMeAvisos, "PATCH", "/auth/me/avisos", `{"avisoEmail":"","avisoTelefone":""}`, true)
	if code != 200 {
		t.Fatalf("PATCH limpar = %d", code)
	}
	code, out = chama(s.handleCreateAviso, "POST", "/avisos", `{"userId":`+jsonInt(uid)+`,"titulo":"x"}`, false)
	if code != 200 || out["telefone"] != "" || out["emailEnviado"] != false {
		t.Fatalf("POST sem e-mail = %d %v", code, out)
	}

	// Conta que não existe.
	code, _ = chama(s.handleCreateAviso, "POST", "/avisos", `{"userId":999999999,"titulo":"x"}`, false)
	if code != http.StatusNotFound {
		t.Errorf("conta inexistente = %d, queria 404", code)
	}
}

func jsonInt(i int64) string { b, _ := json.Marshal(i); return string(b) }
