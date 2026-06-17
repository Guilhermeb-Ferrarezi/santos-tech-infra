package main

import "testing"

func TestCoolifyDeployStatus(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"falha topo", map[string]any{"status": "failed"}, "failed"},
		{"erro na msg", map[string]any{"message": "build ERROR exit 1"}, "failed"},
		{"sucesso", map[string]any{"status": "success"}, "success"},
		{"deployment aninhado", map[string]any{"deployment": map[string]any{"status": "failed"}}, "failed"},
		{"started ignora", map[string]any{"status": "queued"}, "started"},
		{"desconhecido", map[string]any{"foo": "bar"}, ""},
		{"falha tem prioridade", map[string]any{"status": "running", "message": "fail"}, "failed"},
	}
	for _, c := range cases {
		if got := coolifyDeployStatus(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestCoolifyAppName(t *testing.T) {
	if got := coolifyAppName(map[string]any{"application_name": "bot-go"}); got != "bot-go" {
		t.Errorf("got %q", got)
	}
	if got := coolifyAppName(map[string]any{"deployment": map[string]any{"name": "api-go"}}); got != "api-go" {
		t.Errorf("aninhado: got %q", got)
	}
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("abcdef1234567"); got != "abcdef1" {
		t.Errorf("got %q", got)
	}
}

func TestTailLines(t *testing.T) {
	if got := tailLines("a\nb\nc\nd", 2); got != "c\nd" {
		t.Errorf("got %q", got)
	}
	if got := tailLines("a\nb", 0); got != "a\nb" {
		t.Errorf("n<=0 deve devolver tudo: got %q", got)
	}
}
