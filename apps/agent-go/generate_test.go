package main

import (
	"strings"
	"testing"
)

func TestBuildGeneratePrompt(t *testing.T) {
	email := buildGeneratePrompt(generateRequest{Task: "email", Brief: "convite pro curso de robótica"}, false)
	if !strings.Contains(email, "convite pro curso de robótica") {
		t.Error("prompt de email deveria conter o briefing")
	}
	if !strings.Contains(email, `"subject"`) || !strings.Contains(email, `"html"`) {
		t.Error("prompt de email deveria pedir o JSON com subject/html")
	}
	if !strings.Contains(email, "{{name}}") {
		t.Error("prompt deveria mencionar as merge tags")
	}

	subjects := buildGeneratePrompt(generateRequest{Task: "subjects", Brief: "promoção de matrículas"}, false)
	if !strings.Contains(subjects, `"subjects"`) {
		t.Error("prompt de subjects deveria pedir o JSON com subjects")
	}

	rewrite := buildGeneratePrompt(generateRequest{Task: "rewrite", Brief: "deixa mais curto", HTML: "<p>Olá</p>"}, false)
	if !strings.Contains(rewrite, "<p>Olá</p>") || !strings.Contains(rewrite, "deixa mais curto") {
		t.Error("prompt de rewrite deveria conter o html atual e a instrução")
	}

	spam := buildGeneratePrompt(generateRequest{Task: "spamcheck", Subject: "GANHE AGORA", HTML: "<p>oferta</p>"}, false)
	if !strings.Contains(spam, "GANHE AGORA") || !strings.Contains(spam, "<p>oferta</p>") {
		t.Error("prompt de spamcheck deveria conter assunto e html")
	}
	if !strings.Contains(spam, `"score"`) || !strings.Contains(spam, `"issues"`) {
		t.Error("prompt de spamcheck deveria pedir o JSON com score/issues")
	}
}

func TestParseGenerateResult(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantSub string
		wantErr bool
	}{
		{"json puro", `{"subject":"Oi","html":"<p>x</p>","text":"x"}`, "Oi", false},
		{"com cercas", "```json\n{\"subject\":\"Oi\",\"html\":\"<p>x</p>\"}\n```", "Oi", false},
		{"texto ao redor", "Aqui está:\n{\"subject\":\"Oi\",\"html\":\"<p>x</p>\"}\nPronto!", "Oi", false},
		{"invalido", "isso não é json", "", true},
		{"vazio", `{}`, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := parseGenerateResult(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("esperava erro, veio %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			if res.Subject != c.wantSub {
				t.Errorf("subject=%q, queria %q", res.Subject, c.wantSub)
			}
		})
	}

	subjects, err := parseGenerateResult(`{"subjects":["a","b","c"]}`)
	if err != nil || len(subjects.Subjects) != 3 {
		t.Fatalf("subjects: err=%v res=%+v", err, subjects)
	}

	spam, err := parseGenerateResult(`{"score":72,"summary":"ok","issues":[{"level":"alto","text":"muito caps"}]}`)
	if err != nil || spam.Score == nil || *spam.Score != 72 || len(spam.Issues) != 1 {
		t.Fatalf("spamcheck: err=%v res=%+v", err, spam)
	}

	cmd, err := parseGenerateResult(`{"action":"navigate","args":{"page":"logs"},"reply":"Abrindo os logs."}`)
	if err != nil || cmd.Action != "navigate" || cmd.Args["page"] != "logs" || cmd.Reply == "" {
		t.Fatalf("command: err=%v res=%+v", err, cmd)
	}

	multi, err := parseGenerateResult(`{"actions":[{"action":"create_segment","args":{"name":"Alunos"}},{"action":"create_lead","args":{"email":"a@x.com"}}],"reply":"Confirme no painel."}`)
	if err != nil || len(multi.Actions) != 2 || multi.Actions[0].Action != "create_segment" || multi.Actions[1].Args["email"] != "a@x.com" {
		t.Fatalf("command multi: err=%v res=%+v", err, multi)
	}
}

func TestImageExtFromMime(t *testing.T) {
	for mime, want := range map[string]string{
		"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "image/gif": "gif",
	} {
		if got, ok := imageExtFromMime(mime); !ok || got != want {
			t.Errorf("imageExtFromMime(%q)=%q,%v quer %q", mime, got, ok, want)
		}
	}
	if _, ok := imageExtFromMime("application/pdf"); ok {
		t.Error("pdf não deveria ser imagem")
	}
}

func TestNormalizeGenerateCommand(t *testing.T) {
	// command com brief é válido
	req := generateRequest{Task: "command", Brief: "abre os logs"}
	if err := normalizeGenerate(&req); err != nil {
		t.Fatalf("command válido foi rejeitado: %v", err)
	}
	// command sem brief → erro
	empty := generateRequest{Task: "command"}
	if err := normalizeGenerate(&empty); err == nil {
		t.Error("command sem brief deveria falhar")
	}
	// task desconhecida → erro
	bad := generateRequest{Task: "xpto", Brief: "x"}
	if err := normalizeGenerate(&bad); err == nil {
		t.Error("task inválida deveria falhar")
	}
}

func TestBuildCommandPrompt(t *testing.T) {
	p := buildGeneratePrompt(generateRequest{
		Task:    "command",
		Brief:   "me mostra os envios que falharam",
		Context: "Ações:\n- view_logs(status): abre os logs",
	}, false)
	if !strings.Contains(p, "me mostra os envios que falharam") {
		t.Error("prompt de command deveria conter o pedido do usuário")
	}
	if !strings.Contains(p, "view_logs") {
		t.Error("prompt de command deveria conter o catálogo (context)")
	}
	if !strings.Contains(p, `"action"`) || !strings.Contains(p, `"reply"`) {
		t.Error("prompt de command deveria pedir JSON com action/reply")
	}
}

func TestBuildPromptWithImage(t *testing.T) {
	// Sem imagem: proíbe ferramentas.
	noImg := buildGeneratePrompt(generateRequest{Task: "email", Brief: "x"}, false)
	if !strings.Contains(noImg, "NÃO use ferramentas") {
		t.Error("sem imagem deveria proibir ferramentas")
	}
	// Com imagem: NÃO pode proibir ferramentas (senão o modelo recusa o Read) e
	// deve mandar usar o Read.
	withImg := buildGeneratePrompt(generateRequest{Task: "command", Brief: "o que é isso?"}, true)
	if strings.Contains(withImg, "NÃO use ferramentas") {
		t.Error("com imagem NÃO deveria proibir ferramentas")
	}
	if !strings.Contains(withImg, "Read") {
		t.Error("com imagem deveria instruir a usar o Read")
	}
}
