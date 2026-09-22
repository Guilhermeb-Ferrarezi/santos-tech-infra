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
	// MultiplaProb: probabilidade (0–1) de que a questão peça MAIS DE UMA
	// alternativa — resposta da pergunta "noul" própria "multipla" (ver
	// buildJevRequest). Zero quando o Jev não devolveu essa chave (respostas
	// antigas, sem o campo) — o que decide corretamente como "não é
	// múltipla" na falta de sinal.
	MultiplaProb float64
	// AltProbs: probabilidade de CADA alternativa estar correta, indexada
	// pelo rótulo — respostas das perguntas "noul" "alt_<rótulo>", uma por
	// alternativa de p.Order. Nil quando nenhuma delas veio na resposta.
	AltProbs map[string]float64
}

func buildJevRequest(p quizParsed) ([]byte, error) {
	criteria := make(map[string]string, len(p.Options))
	for k, v := range p.Options {
		criteria[k] = v
	}
	questions := map[string]any{
		"resposta": map[string]any{
			"type":         "choice",
			"instructions": "Qual alternativa responde corretamente à questão?",
			"criteria":     criteria,
		},
		// "multipla": pergunta "noul" (probabilidade de "true") própria,
		// respondida na MESMA chamada que "resposta" — o Jev aceita várias
		// perguntas por requisição. Decide se a questão pede marcar mais de
		// uma alternativa (ver quizMultiploThreshold em quiz.go).
		"multipla": map[string]any{
			"type":         "noul",
			"instructions": "A questão pede que MAIS DE UMA alternativa seja assinalada?",
			"criteria": map[string]string{
				"true":  "pede várias alternativas",
				"false": "pede uma única alternativa correta",
			},
		},
	}
	// "alt_<rótulo>": uma pergunta "noul" por alternativa, usando os rótulos
	// REAIS de p.Order (nunca A/B/C genérico — a prova pode rotular 1/2/3, ou
	// letra minúscula). Cada uma pergunta se AQUELA alternativa específica
	// está correta — é isso que vira AltProbs e decide o que marcar no modo
	// múltipla resposta.
	for _, label := range p.Order {
		questions["alt_"+label] = map[string]any{
			"type":         "noul",
			"instructions": fmt.Sprintf("A alternativa %s está correta para esta questão?", label),
			"criteria": map[string]string{
				"true":  fmt.Sprintf("a alternativa «%s» é correta", p.Options[label]),
				"false": fmt.Sprintf("a alternativa «%s» é incorreta", p.Options[label]),
			},
		}
	}
	body, err := json.Marshal(map[string]any{
		"state":     p.Question,
		"model":     "jev-latest",
		"questions": questions,
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

	// multipla/alt_X: sinais de múltipla resposta, OPCIONAIS na resposta —
	// fixtures e respostas antigas do Jev não têm essas chaves, e a ausência
	// tem que resultar em "não é múltipla resposta" (MultiplaProb zero), não
	// erro. Formato medido em produção: {"type":"noul","noul":0.86} — o
	// valor é um FLOAT (probabilidade de "true"), não booleano, e não traz
	// confidence nem probabilities.
	v.MultiplaProb = parseJevNoul(envelope.Answers["multipla"])
	if len(p.Order) > 0 {
		altProbs := make(map[string]float64, len(p.Order))
		for _, label := range p.Order {
			if item, ok := envelope.Answers["alt_"+label]; ok {
				altProbs[label] = parseJevNoul(item)
			}
		}
		if len(altProbs) > 0 {
			v.AltProbs = altProbs
		}
	}
	return v, nil
}

// parseJevNoul extrai o valor de uma resposta do tipo "noul" — probabilidade
// de "true", nunca booleano. raw vazio (chave ausente) ou malformado devolve
// zero: no caso de "multipla" isso decide corretamente como "não é múltipla"
// na falta de sinal; no caso de "alt_X" o rótulo simplesmente não entra em
// AltProbs (ver o guard de len(item)==0 no chamador — chave ausente nem
// chega aqui).
func parseJevNoul(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var item struct {
		Noul float64 `json:"noul"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return 0
	}
	return item.Noul
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
