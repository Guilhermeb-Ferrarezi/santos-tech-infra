package main

// Tradução entre a questão já separada e o protocolo do Jev (TypeSafe AI):
// uma pergunta `choice` cujos `criteria` são as alternativas. O Jev não gera
// texto — devolve a alternativa escolhida com probabilidades, que é o que
// permite decidir se vale escalar pro LLM de texto.

import (
	"encoding/json"
	"fmt"
	"sort"
)

const quizJevPath = "/v1/systemone"

type quizVerdict struct {
	Label         string
	Confidence    float64
	Probabilities map[string]float64
}

func buildJevRequest(p quizParsed) ([]byte, error) {
	criteria := make(map[string]string, len(p.Options))
	for k, v := range p.Options {
		criteria[k] = v
	}
	body, err := json.Marshal(map[string]any{
		"state": p.Question,
		"model": "jev-latest",
		"questions": map[string]any{
			"resposta": map[string]any{
				"type":         "choice",
				"instructions": "Qual alternativa responde corretamente à questão?",
				"criteria":     criteria,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("quiz: montar corpo do jev: %w", err)
	}
	return body, nil
}

func parseJevResponse(raw []byte, p quizParsed) (quizVerdict, error) {
	var envelope struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return quizVerdict{}, fmt.Errorf("quiz: resposta do jev ilegível: %w", err)
	}
	item, ok := envelope.Answers["resposta"]
	if !ok {
		return quizVerdict{}, fmt.Errorf("quiz: resposta do jev sem a chave 'resposta'")
	}
	var ans struct {
		Choice        string             `json:"choice"`
		Value         string             `json:"value"`
		Answer        string             `json:"answer"`
		Confidence    float64            `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	}
	if err := json.Unmarshal(item, &ans); err != nil {
		return quizVerdict{}, fmt.Errorf("quiz: item de resposta do jev ilegível: %w", err)
	}
	v := quizVerdict{
		Label:         firstNonEmpty(ans.Choice, ans.Value, ans.Answer),
		Confidence:    ans.Confidence,
		Probabilities: ans.Probabilities,
	}
	if v.Label == "" {
		// Sem valor explícito, a maior probabilidade decide.
		v.Label = maxProbLabel(ans.Probabilities)
	}
	if v.Label == "" {
		return quizVerdict{}, fmt.Errorf("quiz: jev não devolveu alternativa")
	}
	if _, ok := p.Options[v.Label]; !ok {
		// Rótulo fora do conjunto enviado: responder isso seria pior que falhar,
		// porque a extensão mostraria uma letra que não existe na prova.
		return quizVerdict{}, fmt.Errorf("quiz: jev devolveu rótulo desconhecido %q", v.Label)
	}
	if v.Confidence == 0 {
		v.Confidence = v.Probabilities[v.Label]
	}
	return v, nil
}

func (v quizVerdict) margin() float64 {
	if len(v.Probabilities) < 2 {
		return 1
	}
	vals := make([]float64, 0, len(v.Probabilities))
	for _, p := range v.Probabilities {
		vals = append(vals, p)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(vals)))
	return vals[0] - vals[1]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func maxProbLabel(probs map[string]float64) string {
	best, bestP := "", -1.0
	for label, p := range probs {
		// Desempate por rótulo mantém o resultado determinístico: mapa em Go
		// não tem ordem, e sem isso o mesmo input daria respostas diferentes.
		if p > bestP || (p == bestP && label < best) {
			best, bestP = label, p
		}
	}
	return best
}
