package main

import (
	"encoding/json"
	"os"
	"testing"
)

func exemploParsed() quizParsed {
	return quizParsed{
		Question: "Qual a capital da Mongólia?",
		Options:  map[string]string{"A": "Astana", "B": "Ulan Bator", "C": "Bishkek"},
		Order:    []string{"A", "B", "C"},
	}
}

func TestBuildJevRequest(t *testing.T) {
	body, err := buildJevRequest(exemploParsed())
	if err != nil {
		t.Fatalf("buildJevRequest: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("corpo inválido: %v", err)
	}
	if got["state"] != "Qual a capital da Mongólia?" {
		t.Errorf("state = %v", got["state"])
	}
	if got["model"] != "jev-latest" {
		t.Errorf("model = %v", got["model"])
	}
	q := got["questions"].(map[string]any)["resposta"].(map[string]any)
	if q["type"] != "choice" {
		t.Errorf("type = %v", q["type"])
	}
	crit := q["criteria"].(map[string]any)
	if len(crit) != 3 || crit["B"] != "Ulan Bator" {
		t.Errorf("criteria = %v", crit)
	}
}

// O fixture é a resposta REAL da API, capturada na calibração. Se a TypeSafe
// mudar o formato, este teste quebra — que é exatamente o que se quer.
func TestParseJevResponseFixtureReal(t *testing.T) {
	raw, err := os.ReadFile("../../docs/superpowers/specs/fixtures/jev-resposta-exemplo.json")
	if err != nil {
		t.Fatalf("fixture da Task 0 ausente: %v", err)
	}
	v, err := parseJevResponse(raw, exemploParsed())
	if err != nil {
		t.Fatalf("parseJevResponse: %v", err)
	}
	if v.Label == "" {
		t.Error("nenhuma alternativa extraída da resposta real")
	}
	if _, ok := exemploParsed().Options[v.Label]; !ok {
		t.Errorf("alternativa %q não é um dos rótulos enviados", v.Label)
	}
}

func TestParseJevResponseConfiancaVemDasProbabilidades(t *testing.T) {
	// Quando o Jev não manda `confidence`, a confiança é a probabilidade da
	// alternativa escolhida — senão toda resposta viraria "confiança zero" e
	// escalaria sempre, jogando fora o barato do Jev.
	raw := []byte(`{"answers":{"resposta":{"type":"choice","choice":"B","probabilities":{"A":0.1,"B":0.7,"C":0.2}}}}`)
	v, err := parseJevResponse(raw, exemploParsed())
	if err != nil {
		t.Fatalf("parseJevResponse: %v", err)
	}
	if v.Label != "B" {
		t.Errorf("label = %q", v.Label)
	}
	if v.Confidence < 0.69 || v.Confidence > 0.71 {
		t.Errorf("confidence = %v, queria ~0.7", v.Confidence)
	}
	if m := v.margin(); m < 0.49 || m > 0.51 {
		t.Errorf("margin = %v, queria ~0.5", m)
	}
}

func TestParseJevResponseRotuloDesconhecido(t *testing.T) {
	raw := []byte(`{"answers":{"resposta":{"type":"choice","choice":"Z","confidence":0.9}}}`)
	if _, err := parseJevResponse(raw, exemploParsed()); err == nil {
		t.Error("queria erro: Z não é um rótulo enviado")
	}
}

func TestMarginComUmaProbabilidade(t *testing.T) {
	v := quizVerdict{Label: "A", Probabilities: map[string]float64{"A": 0.8}}
	if v.margin() != 1 {
		t.Errorf("margin = %v, queria 1 (não há segunda alternativa pra empatar)", v.margin())
	}
}
