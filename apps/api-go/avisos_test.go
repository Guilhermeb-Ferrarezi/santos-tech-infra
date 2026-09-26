package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Contatos de aviso da conta e o POST /avisos — o caminho que faltava para o
// responsável de um retorno ao cliente ser avisado de verdade (a Tarefa criada
// pela própria conta não notifica quem a criou).

func TestNormalizaAvisoEmail(t *testing.T) {
	casos := []struct {
		in   string
		want string // "" com ok = limpar
		ok   bool
	}{
		{"henrique@gmail.com", "henrique@gmail.com", true},
		{"  Henrique@Gmail.com ", "henrique@gmail.com", true},
		{"", "", true},
		{"   ", "", true},
		{"sem-arroba", "", false},
		{"Henrique <h@gmail.com>", "", false}, // só o endereço, sem nome
		{"a@b", "", false},                    // sem domínio de verdade
		{strings.Repeat("a", 250) + "@x.com", "", false},
	}
	for _, c := range casos {
		got, err := normalizaAvisoEmail(c.in)
		if (err == nil) != c.ok {
			t.Errorf("normalizaAvisoEmail(%q): err=%v, queria ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("normalizaAvisoEmail(%q) = %q, queria %q", c.in, got, c.want)
		}
	}
}

func TestNormalizaAvisoTelefone(t *testing.T) {
	casos := []struct {
		in   string
		want string
		ok   bool
	}{
		{"5516999990000", "5516999990000", true},
		{"+55 (16) 99999-0000", "5516999990000", true},
		// Sem o 55: número do Brasil (DDD + número, 10 ou 11 dígitos) ganha o 55.
		{"16999990000", "5516999990000", true},
		{"(16) 99999-0000", "5516999990000", true},
		{"1633334444", "551633334444", true},
		{"", "", true},
		{"123", "", false},
		{"1234567890123456", "", false},
		{"16 9999x0000", "", false}, // letra no meio não é telefone
	}
	for _, c := range casos {
		got, err := normalizaAvisoTelefone(c.in)
		if (err == nil) != c.ok {
			t.Errorf("normalizaAvisoTelefone(%q): err=%v, queria ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("normalizaAvisoTelefone(%q) = %q, queria %q", c.in, got, c.want)
		}
	}
}

func TestValidaAvisoInput(t *testing.T) {
	ok := avisoInput{UserID: 30, Titulo: "Retomar Vivian", Corpo: "texto", URL: "https://santos-tech.com/dashboard/admin/whats/conversas?c=1"}
	if err := validaAvisoInput(ok); err != nil {
		t.Fatalf("válido deu erro: %v", err)
	}
	for nome, in := range map[string]avisoInput{
		"sem usuário":      {Titulo: "x"},
		"sem título":       {UserID: 30},
		"título enorme":    {UserID: 30, Titulo: strings.Repeat("a", 200)},
		"corpo enorme":     {UserID: 30, Titulo: "x", Corpo: strings.Repeat("a", 5000)},
		"link de fora":     {UserID: 30, Titulo: "x", URL: "https://golpe.com/pix"},
		"link parecido":    {UserID: 30, Titulo: "x", URL: "https://santos-tech.com.golpe.com/"},
		"link javascript":  {UserID: 30, Titulo: "x", URL: "javascript:alert(1)"},
		"link sem esquema": {UserID: 30, Titulo: "x", URL: "//golpe.com"},
		// userId acima de math.MaxInt32: GetUserAvisos/notifyUser recebem
		// int32, então sem este teto o cast trunca (dois complementos) e o
		// aviso vai pra conta errada em vez de falhar com 400.
		"userId acima de int32": {UserID: 1<<32 + 1, Titulo: "x"},
	} {
		if err := validaAvisoInput(in); err == nil {
			t.Errorf("%s: deveria ser inválido", nome)
		}
	}
	for _, u := range []string{"", "/dashboard/tarefas", "https://api.santos-tech.com/x", "https://santos-tech.com"} {
		in := avisoInput{UserID: 30, Titulo: "x", URL: u}
		if err := validaAvisoInput(in); err != nil {
			t.Errorf("link %q deveria valer: %v", u, err)
		}
	}
}

// Assunto de e-mail: um \r\n no título injetaria cabeçalho SMTP arbitrário.
func TestStripCRLF(t *testing.T) {
	casos := map[string]string{
		"Retomar Vivian":           "Retomar Vivian",
		"x\r\nBcc: golpe@evil.com": "xBcc: golpe@evil.com",
		"linha1\nlinha2\rlinha3":   "linha1linha2linha3",
	}
	for in, want := range casos {
		if got := stripCRLF(in); got != want {
			t.Errorf("stripCRLF(%q) = %q, queria %q", in, got, want)
		}
	}
}

// O corpo vem do bot (texto do cliente dentro): não pode virar HTML.
func TestEmailDeAvisoEscapa(t *testing.T) {
	html := emailDeAviso("Retomar <b>Ana</b>", "disse: <script>x</script>\nlinha 2", "https://santos-tech.com/dashboard")
	if strings.Contains(html, "<script>") || strings.Contains(html, "<b>Ana</b>") {
		t.Errorf("HTML não escapado:\n%s", html)
	}
	if !strings.Contains(html, "linha 2") || !strings.Contains(html, "<br>") || !strings.Contains(html, `href="https://santos-tech.com/dashboard"`) {
		t.Errorf("faltou quebra de linha ou link:\n%s", html)
	}
}

// Caminho de erro das rotas, antes de tocar no banco.
func TestRotasDeAvisoSemSessao(t *testing.T) {
	s := testServer(Config{})
	for _, c := range []struct {
		h    http.HandlerFunc
		met  string
		path string
	}{
		{s.authGuard(s.handleGetMeAvisos), "GET", "/auth/me/avisos"},
		{s.authGuard(s.handlePatchMeAvisos), "PATCH", "/auth/me/avisos"},
		{s.adminGuard(s.handleCreateAviso), "POST", "/avisos"},
	} {
		w := httptest.NewRecorder()
		c.h(w, httptest.NewRequest(c.met, c.path, strings.NewReader(`{}`)))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s sem sessão = %d, queria 401", c.met, c.path, w.Code)
		}
	}
}

func TestPatchMeAvisosCorpoInvalido(t *testing.T) {
	s := testServer(Config{})
	for _, body := range []string{"xxx", `{"avisoEmail":"sem-arroba"}`, `{"avisoTelefone":"123"}`} {
		w := httptest.NewRecorder()
		s.handlePatchMeAvisos(w, httptest.NewRequest("PATCH", "/auth/me/avisos", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, queria 400", body, w.Code)
		}
	}
}

func TestCreateAvisoCorpoInvalido(t *testing.T) {
	s := testServer(Config{})
	for _, body := range []string{"xxx", `{"userId":0,"titulo":"x"}`, `{"userId":30,"titulo":"x","url":"https://golpe.com"}`} {
		w := httptest.NewRecorder()
		s.handleCreateAviso(w, httptest.NewRequest("POST", "/avisos", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, queria 400", body, w.Code)
		}
	}
}
