package main

import "testing"

func TestValidateOAuthClientInput(t *testing.T) {
	cases := []struct {
		clientID string
		uris     []string
		ok       bool
	}{
		{"meu-app", []string{"https://app.santos-tech.com/callback"}, true},
		{"MeuApp_2", []string{"https://x.com/cb"}, true},
		{"", []string{"https://x.com/cb"}, false},           // client_id vazio
		{"tem espaço", []string{"https://x.com/cb"}, false}, // chars inválidos
		{"ok", nil, false},                                                  // sem redirect
		{"ok", []string{"notaurl"}, false},                                  // uri inválida
		{"ok", []string{"https://x.com/cb", ""}, false},                     // uri vazia
		{"ok", []string{"javascript://santos-tech.com/%0aalert(1)"}, false}, // scheme perigoso
		{"ok", []string{"data:text/html,x"}, false},                         // scheme perigoso
		{"ok", []string{"ftp://x.com/cb"}, false},                           // só http(s)
		{"ok", []string{"http://localhost:9999/cb"}, true},                  // http permitido (dev)
	}
	for _, c := range cases {
		err := validateOAuthClientInput(c.clientID, c.uris)
		if (err == nil) != c.ok {
			t.Errorf("clientID=%q uris=%v: err=%v, esperava ok=%v", c.clientID, c.uris, err, c.ok)
		}
	}
}
