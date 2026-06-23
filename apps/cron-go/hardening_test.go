package main

import (
	"context"
	"testing"

	"github.com/santos-tech/cron-go/db"
)

func TestBuildTargetURLRawRequiresHTTPS(t *testing.T) {
	s := &Server{cfg: Config{AllowRawHTTP: true}}

	// http:// no modo raw deve ser rejeitado (Bearer não pode ir em plaintext).
	if _, _, err := s.buildTargetURL(db.CronJob{ActionKind: "http", HttpUrl: "http://payments.santos-tech.com/x"}); err == nil {
		t.Fatal("esperava erro para URL http:// crua")
	}
	// https:// passa.
	m, u, err := s.buildTargetURL(db.CronJob{ActionKind: "http", HttpMethod: "POST", HttpUrl: "https://payments.santos-tech.com/x"})
	if err != nil {
		t.Fatalf("https deveria passar, veio erro: %v", err)
	}
	if m != "POST" || u != "https://payments.santos-tech.com/x" {
		t.Fatalf("método/URL inesperados: %s %s", m, u)
	}

	// Com AllowRawHTTP=false, qualquer raw é rejeitado.
	s2 := &Server{cfg: Config{AllowRawHTTP: false}}
	if _, _, err := s2.buildTargetURL(db.CronJob{ActionKind: "http", HttpUrl: "https://payments.santos-tech.com/x"}); err == nil {
		t.Fatal("esperava erro com AllowRawHTTP=false")
	}
}

func TestUserFromContext(t *testing.T) {
	if got := userFromContext(context.Background()); got != "" {
		t.Fatalf("esperava vazio sem valor, veio %q", got)
	}
	ctx := context.WithValue(context.Background(), userCtxKey, "admin@santos-tech.com")
	if got := userFromContext(ctx); got != "admin@santos-tech.com" {
		t.Fatalf("esperava o email, veio %q", got)
	}
}

func TestJobInputValidateMoreCases(t *testing.T) {
	base := jobInput{Name: "x", ActionKind: "catalog", ActionRef: "payments.gerar-cobrancas-mes", ScheduleCron: "0 9 * * *", Timezone: "America/Sao_Paulo"}

	cases := []struct {
		name string
		mut  func(j *jobInput)
		ok   bool
	}{
		{"válido", func(*jobInput) {}, true},
		{"sem nome", func(j *jobInput) { j.Name = "" }, false},
		{"cron inválido", func(j *jobInput) { j.ScheduleCron = "lixo" }, false},
		{"timezone inválida", func(j *jobInput) { j.Timezone = "Mars/Phobos" }, false},
		{"catálogo desconhecido", func(j *jobInput) { j.ActionRef = "nao.existe" }, false},
		{"actionKind inválido", func(j *jobInput) { j.ActionKind = "weird" }, false},
	}
	for _, c := range cases {
		j := base
		c.mut(&j)
		if _, ok := j.validate(false); ok != c.ok {
			t.Errorf("%s: validate ok=%v, queria %v", c.name, ok, c.ok)
		}
	}
}
