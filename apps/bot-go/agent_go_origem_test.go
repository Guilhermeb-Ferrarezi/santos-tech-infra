package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// O agent-go separa o gasto do bot pela origem, e é ela que decide se o bot roda
// com a chave de API: resposta ao cliente = "bot", chamadas internas em haiku
// (resumo, ficha da base, follow-up, reativação) = "bot-tarefas".
func TestAgentGoMandaAOrigemDoBot(t *testing.T) {
	var origens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		o, _ := body["origin"].(string)
		origens = append(origens, o)
		_, _ = w.Write([]byte(`{"text":"{\"bubbles\":[\"oi\"]}"}`))
	}))
	defer srv.Close()

	c := NewAgentGoClient(srv.URL, "segredo", "sonnet", nil, nil)
	_, _ = c.Respond(context.Background(), Conversation{}, ConversationContext{}, TenantConfig{}, "olá")
	if _, err := c.RespondWithModel(context.Background(), "resuma", "haiku", false); err != nil {
		t.Fatal(err)
	}

	if len(origens) != 2 || origens[0] != "bot" || origens[1] != "bot-tarefas" {
		t.Fatalf("origens enviadas = %q, esperava [bot bot-tarefas]", origens)
	}
}
