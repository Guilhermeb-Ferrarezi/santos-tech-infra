# Extensão de questões (Jev + fallback Claude) — Plano de Implementação

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Selecionar uma questão de múltipla escolha em qualquer página do Zen, apertar `Alt+Q` e receber a alternativa correta num overlay, respondida pelo Jev com escalonamento para o Claude quando a confiança for baixa.

**Architecture:** Uma extensão WebExtension burra (captura a seleção, desenha o overlay) conversa com uma rota nova `POST /quiz/answer` no `api-go`. A rota faz todo o trabalho: separa enunciado de alternativas, pergunta ao Jev via API Router (que já cifra chaves e rotaciona), decide se escala para o Claude, e devolve uma resposta única. Nenhuma credencial de API entra no navegador.

**Tech Stack:** Go 1.x (`apps/api-go`, stdlib `net/http` + `mux.HandleFunc`, testes com `testing` puro, sem testify) · WebExtension MV3 Firefox em JavaScript vanilla, sem bundler e sem dependências.

**Spec:** `docs/superpowers/specs/2026-09-22-extensao-quiz-jev-design.md`

## Global Constraints

- **Nunca** colocar chave de API, token ou senha em arquivo do repositório. As chaves do Jev e da Anthropic são cadastradas pela UI admin do API Router, que as cifra no banco.
- Antes de **qualquer** commit, push ou deploy que toque Go: `gofmt -l .` (saída vazia) · `go vet ./...` · `go build ./...` · `go test ./...`. Regra do `CLAUDE.md` do repositório — build quebrado é deploy quebrado.
- Mensagens de erro, comentários e nomes de teste em **português**, como o resto do `apps/api-go`.
- Testes em `package main`, com `testing` da stdlib. Sem testify, sem mocks gerados.
- Commits sem `Co-Authored-By: Claude` são a preferência do usuário para outros repositórios; **neste** repositório seguir o rodapé pedido pelo harness da sessão.
- A extensão não tem build step: JavaScript vanilla, carregado por `about:debugging`.
- Branch de trabalho: `feat/quiz-jev-extension` (já criada, já contém o spec).

---

### Task 0: Calibração do Jev (bloqueia todas as outras tasks)

Esta task não produz código de produção. Ela existe para responder uma pergunta que, se for respondida errado, invalida o resto do plano: **o Jev acerta questões de múltipla escolha?** Ele é um classificador de julgamento tipado, não uma base de conhecimento. Nenhuma evidência existe ainda.

**Files:**
- Create: `<scratchpad>/calibra_jev.sh` (script descartável, **fora do repositório**)
- Create: `docs/superpowers/specs/fixtures/jev-resposta-exemplo.json` (fixture real, esta sim entra no repo)

**Interfaces:**
- Consumes: nada.
- Produces: `docs/superpowers/specs/fixtures/jev-resposta-exemplo.json` — a resposta crua do Jev para uma pergunta `choice`, usada como fixture de teste na Task 2. Produz também os valores calibrados de `QUIZ_MIN_CONFIDENCE` e `QUIZ_MIN_MARGIN`.

**Pré-requisitos que vêm do usuário (pedir, não inventar):**
1. A API key do Jev. Não está salva em lugar nenhum — a memória `reference_typesafe_jev_api` registra que ele a colou uma vez no chat e que se deve pedir de novo. **A key fica só na variável de ambiente do shell; nunca escrita em arquivo.**
2. ~20 questões reais com gabarito, misturando fáceis, de conhecimento factual, e de cálculo/raciocínio em etapas.

- [ ] **Step 1: Exportar a key no shell (nada em arquivo)**

```bash
read -s JEV_KEY && export JEV_KEY
```

- [ ] **Step 2: Escrever o script de uma questão, para descobrir o formato real da resposta**

O formato do campo de valor de uma resposta `choice` não está documentado na memória (ela registra `{"type":..., "<valor>":..., "probabilities"?, "confidence"?}`, com a chave variando por tipo). Descobrir, não adivinhar:

```bash
cat > "$SCRATCH/calibra_jev.sh" <<'SH'
#!/usr/bin/env bash
# Uso: ./calibra_jev.sh "<enunciado>" "<altA>" "<altB>" "<altC>" "<altD>"
set -euo pipefail
enunciado="$1"; shift
criteria=$(jq -n '$ARGS.positional | to_entries | map({ key: (["A","B","C","D","E"][.key]), value: .value }) | from_entries' --args "$@")
jq -n --arg state "$enunciado" --argjson criteria "$criteria" '{
  state: $state,
  model: "jev-latest",
  questions: {
    resposta: {
      type: "choice",
      instructions: "Qual alternativa responde corretamente à questão?",
      criteria: $criteria
    }
  }
}' | curl -sS -X POST https://api.typesafe.ai/v1/systemone \
  -H "Authorization: Bearer $JEV_KEY" \
  -H "Content-Type: application/json" \
  --data-binary @-
SH
chmod +x "$SCRATCH/calibra_jev.sh"
```

- [ ] **Step 3: Rodar uma questão e inspecionar a resposta crua**

```bash
"$SCRATCH/calibra_jev.sh" "Qual a capital da Mongólia?" "Astana" "Ulan Bator" "Bishkek" "Tashkent" | tee "$SCRATCH/resposta-crua.json" | jq .
```

Observar: qual é a chave que carrega a alternativa escolhida (`choice`? `value`? `answer`?), se `probabilities` e `confidence` vêm sempre ou só às vezes, e se as chaves de `probabilities` são os rótulos que mandamos.

- [ ] **Step 4: Salvar a resposta real como fixture do repositório**

```bash
mkdir -p docs/superpowers/specs/fixtures
cp "$SCRATCH/resposta-crua.json" docs/superpowers/specs/fixtures/jev-resposta-exemplo.json
```

A Task 2 escreve o parser contra **este arquivo**, não contra um formato imaginado.

- [ ] **Step 5: Rodar as ~20 questões e tabular**

Para cada questão: rodar o script, extrair a alternativa escolhida, a confiança, e as duas maiores probabilidades. Montar uma tabela `questão | gabarito | resposta do Jev | acertou? | confidence | p1−p2`.

- [ ] **Step 6: Decidir o caminho, e dizer ao usuário qual foi**

| Resultado | Decisão |
|---|---|
| Acerto alto (≳80%) | Segue o plano como está. `QUIZ_MIN_CONFIDENCE` = a confiança abaixo da qual os erros se concentram; `QUIZ_MIN_MARGIN` = a margem típica dos erros. |
| Acerto baixo, **mas** erra com confiança baixa | Segue o plano com limiar alto (o Jev vira filtro barato, o Claude vira a resposta). Registrar o limiar escolhido. |
| Acerto baixo **e** erra com confiança alta | **Parar.** O Jev não serve para este caso. Reportar ao usuário e propor a variante: a rota chama o Claude direto, e as Tasks 2 e 3 viram uma só. Não construir o desvio pelo Jev. |

- [ ] **Step 7: Commit do fixture e do resultado**

```bash
git add docs/superpowers/specs/fixtures/jev-resposta-exemplo.json
git commit -m "docs: fixture real da resposta do Jev e resultado da calibração"
```

Registrar no corpo do commit a tabela resumida e os limiares escolhidos.

---

### Task 1: Parser da questão

Separa o bloco cru selecionado em enunciado + alternativas. É a peça que mais vai quebrar em site novo, e é 100% testável sem rede.

**Files:**
- Create: `apps/api-go/quiz_parse.go`
- Test: `apps/api-go/quiz_parse_test.go`

**Interfaces:**
- Consumes: nada.
- Produces:
  ```go
  type quizParsed struct {
      Question string            `json:"question"`
      Options  map[string]string `json:"options"`
      Order    []string          `json:"-"` // rótulos na ordem original
  }
  var errQuizUnparseable = errors.New("quiz: não foi possível separar as alternativas")
  func parseQuizBlock(raw string) (quizParsed, error)
  ```

- [ ] **Step 1: Escrever os testes que falham**

```go
package main

import "testing"

func TestParseQuizBlockRotulosComParenteses(t *testing.T) {
	raw := "Qual a capital da Mongólia?\nA) Astana\nB) Ulan Bator\nC) Bishkek"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	if got.Question != "Qual a capital da Mongólia?" {
		t.Errorf("enunciado = %q", got.Question)
	}
	if len(got.Options) != 3 || got.Options["B"] != "Ulan Bator" {
		t.Errorf("alternativas = %v", got.Options)
	}
	if len(got.Order) != 3 || got.Order[0] != "A" || got.Order[2] != "C" {
		t.Errorf("ordem = %v", got.Order)
	}
}

func TestParseQuizBlockPreservaRotuloNumerico(t *testing.T) {
	raw := "Quanto é 2+2?\n1. Três\n2. Quatro\n3. Cinco"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	// O rótulo devolvido tem que ser o da prova, não uma letra inventada:
	// a pessoa marca "2" no cartão, não "B".
	if got.Options["2"] != "Quatro" {
		t.Errorf("alternativas = %v", got.Options)
	}
}

func TestParseQuizBlockAlternativaDeVariasLinhas(t *testing.T) {
	raw := "Questão longa?\nA) primeira parte\ncontinuação da A\nB) segunda"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	if got.Options["A"] != "primeira parte continuação da A" {
		t.Errorf("A = %q", got.Options["A"])
	}
}

func TestParseQuizBlockEnunciadoDeVariasLinhas(t *testing.T) {
	raw := "Considere o texto abaixo.\nEle descreve um caso.\n(a) certo\n(b) errado"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	if got.Question != "Considere o texto abaixo. Ele descreve um caso." {
		t.Errorf("enunciado = %q", got.Question)
	}
	if got.Options["a"] != "certo" {
		t.Errorf("alternativas = %v", got.Options)
	}
}

func TestParseQuizBlockSemRotuloUsaPergunta(t *testing.T) {
	raw := "Qual é a cor do céu?\nAzul\nVerde\nVermelho"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	if got.Question != "Qual é a cor do céu?" {
		t.Errorf("enunciado = %q", got.Question)
	}
	if len(got.Options) != 3 || got.Options["A"] != "Azul" {
		t.Errorf("alternativas geradas = %v", got.Options)
	}
}

func TestParseQuizBlockPoucasAlternativas(t *testing.T) {
	if _, err := parseQuizBlock("Só um texto solto sem alternativa nenhuma"); err != errQuizUnparseable {
		t.Errorf("err = %v, queria errQuizUnparseable", err)
	}
}

func TestParseQuizBlockAlternativasDemais(t *testing.T) {
	raw := "Pergunta?\n1. a\n2. b\n3. c\n4. d\n5. e\n6. f\n7. g\n8. h\n9. i\n1. j"
	if _, err := parseQuizBlock(raw); err != errQuizUnparseable {
		t.Errorf("err = %v, queria errQuizUnparseable (rótulo repetido/acima do limite)", err)
	}
}
```

- [ ] **Step 2: Rodar os testes e confirmar que falham**

Run: `cd apps/api-go && go test ./... -run TestParseQuizBlock -v`
Expected: FAIL com `undefined: parseQuizBlock`

- [ ] **Step 3: Implementar o parser**

```go
package main

// Parser do bloco de texto que o usuário selecionou na página: separa o
// enunciado das alternativas. Fica no servidor de propósito — é a parte que
// mais quebra em site novo, e corrigir aqui é um deploy; corrigir na extensão
// seria reinstalar a extensão no meio do uso.

import (
	"errors"
	"regexp"
	"strings"
)

var errQuizUnparseable = errors.New("quiz: não foi possível separar as alternativas")

type quizParsed struct {
	Question string            `json:"question"`
	Options  map[string]string `json:"options"`
	Order    []string          `json:"-"`
}

const (
	quizMinOptions = 2
	// Teto de 9 acompanha o rótulo de um dígito da regex: uma prova com 10+
	// alternativas não existe na prática, e aceitar mais só abriria espaço pra
	// interpretar lista comum como questão.
	quizMaxOptions = 9
)

// quizLabelRe casa o rótulo no início da linha: "A)", "(a", "1.", "3 -", "b:".
var quizLabelRe = regexp.MustCompile(`^\s*\(?([A-Ea-e]|[1-9])\s*[\)\.\-:]\s+(.*)$`)

func parseQuizBlock(raw string) (quizParsed, error) {
	lines := quizCleanLines(raw)
	if len(lines) < quizMinOptions+1 {
		return quizParsed{}, errQuizUnparseable
	}
	if p, err := parseQuizLabeled(lines); err == nil {
		return p, nil
	}
	return parseQuizUnlabeled(lines)
}

// quizCleanLines normaliza quebras de linha, tira bullets e descarta linhas vazias.
func quizCleanLines(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimLeft(l, "•·–—*• ")
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// parseQuizLabeled: caminho normal, alternativas com rótulo explícito. A
// primeira linha rotulada fecha o enunciado; linhas sem rótulo depois disso
// são continuação da alternativa anterior.
func parseQuizLabeled(lines []string) (quizParsed, error) {
	p := quizParsed{Options: map[string]string{}}
	var enunciado []string
	current := ""
	for _, l := range lines {
		m := quizLabelRe.FindStringSubmatch(l)
		if m == nil {
			if current == "" {
				enunciado = append(enunciado, l)
			} else {
				p.Options[current] += " " + l
			}
			continue
		}
		label, text := m[1], strings.TrimSpace(m[2])
		if _, dup := p.Options[label]; dup {
			// Rótulo repetido = o bloco tem mais de uma questão, ou não é
			// questão nenhuma. Melhor recusar que responder a questão errada.
			return quizParsed{}, errQuizUnparseable
		}
		p.Options[label] = text
		p.Order = append(p.Order, label)
		current = label
	}
	if len(p.Options) < quizMinOptions || len(p.Options) > quizMaxOptions {
		return quizParsed{}, errQuizUnparseable
	}
	p.Question = strings.Join(enunciado, " ")
	if p.Question == "" {
		return quizParsed{}, errQuizUnparseable
	}
	return p, nil
}

// parseQuizUnlabeled: sem rótulo nenhum. Só aceita quando a primeira linha é
// claramente uma pergunta — senão qualquer lista da página viraria "questão".
func parseQuizUnlabeled(lines []string) (quizParsed, error) {
	if !strings.HasSuffix(lines[0], "?") {
		return quizParsed{}, errQuizUnparseable
	}
	opts := lines[1:]
	if len(opts) < quizMinOptions || len(opts) > quizMaxOptions {
		return quizParsed{}, errQuizUnparseable
	}
	p := quizParsed{Question: lines[0], Options: map[string]string{}}
	for i, text := range opts {
		label := string(rune('A' + i))
		p.Options[label] = text
		p.Order = append(p.Order, label)
	}
	return p, nil
}
```

- [ ] **Step 4: Rodar os testes e confirmar que passam**

Run: `cd apps/api-go && go test ./... -run TestParseQuizBlock -v`
Expected: PASS em todos

- [ ] **Step 5: Verificação obrigatória e commit**

```bash
cd apps/api-go && gofmt -l . && go vet ./... && go build ./... && go test ./...
git add apps/api-go/quiz_parse.go apps/api-go/quiz_parse_test.go
git commit -m "feat(quiz): parser do bloco de questão selecionado"
```

---

### Task 2: Requisição e resposta do Jev

Monta a pergunta `choice` e lê a resposta. O parser da resposta é escrito contra o **fixture real** gerado na Task 0 — não contra um formato imaginado.

**Files:**
- Create: `apps/api-go/quiz_jev.go`
- Test: `apps/api-go/quiz_jev_test.go`
- Read: `docs/superpowers/specs/fixtures/jev-resposta-exemplo.json` (da Task 0)

**Interfaces:**
- Consumes: `quizParsed` (Task 1).
- Produces:
  ```go
  const quizJevPath = "/v1/systemone"
  type quizVerdict struct {
      Label         string
      Confidence    float64
      Probabilities map[string]float64
  }
  func buildJevRequest(p quizParsed) ([]byte, error)
  func parseJevResponse(raw []byte, p quizParsed) (quizVerdict, error)
  func (v quizVerdict) margin() float64   // p1 − p2; 1.0 quando há só uma probabilidade
  ```

- [ ] **Step 1: Escrever os testes que falham**

Ajustar os literais de `respostaExemplo` abaixo ao que a Task 0 gravou no fixture, **mantendo as asserções**:

```go
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
```

- [ ] **Step 2: Rodar os testes e confirmar que falham**

Run: `cd apps/api-go && go test ./... -run 'TestBuildJevRequest|TestParseJevResponse|TestMargin' -v`
Expected: FAIL com `undefined: buildJevRequest`

- [ ] **Step 3: Implementar**

```go
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
```

- [ ] **Step 4: Rodar os testes e confirmar que passam**

Run: `cd apps/api-go && go test ./... -run 'TestBuildJevRequest|TestParseJevResponse|TestMargin' -v`
Expected: PASS

- [ ] **Step 5: Verificação obrigatória e commit**

```bash
cd apps/api-go && gofmt -l . && go vet ./... && go build ./... && go test ./...
git add apps/api-go/quiz_jev.go apps/api-go/quiz_jev_test.go
git commit -m "feat(quiz): montagem e leitura da pergunta choice do Jev"
```

---

### Task 3: Fallback Claude

Prompt e parse do escalonamento. Reaproveita o adapter `anthropic` que já existe no API Router — não fala com a Anthropic diretamente.

**Files:**
- Create: `apps/api-go/quiz_fallback.go`
- Test: `apps/api-go/quiz_fallback_test.go`
- Read: `apps/api-go/apirouter_adapters.go:33` (`buildChatRequest`)

**Interfaces:**
- Consumes: `quizParsed` (Task 1).
- Produces:
  ```go
  type quizFallbackAnswer struct {
      Label     string `json:"answer"`
      Reasoning string `json:"reasoning"`
  }
  func buildFallbackPrompt(p quizParsed) string
  func parseFallbackAnswer(texto string, p quizParsed) (quizFallbackAnswer, error)
  ```
  `parseFallbackAnswer` recebe **texto cru** — o que `claudeRaw` devolve. O fallback não passa pelo API Router: o `api-go` pede o texto ao `agent-go` (Claude Code em container), que já roda com a assinatura da empresa e dispensa chave de API.

- [ ] **Step 1: Escrever os testes que falham**

```go
package main

import (
	"strings"
	"testing"
)

func TestBuildFallbackPromptListaAlternativasNaOrdem(t *testing.T) {
	p := quizParsed{
		Question: "Qual a capital da Mongólia?",
		Options:  map[string]string{"A": "Astana", "B": "Ulan Bator"},
		Order:    []string{"A", "B"},
	}
	got := buildFallbackPrompt(p)
	if !strings.Contains(got, "Qual a capital da Mongólia?") {
		t.Error("prompt sem o enunciado")
	}
	iA, iB := strings.Index(got, "A) Astana"), strings.Index(got, "B) Ulan Bator")
	if iA < 0 || iB < 0 || iA > iB {
		// Ordem embaralhada muda a resposta de um LLM; Order existe pra isso.
		t.Errorf("alternativas fora de ordem no prompt:\n%s", got)
	}
	if !strings.Contains(got, "JSON") {
		t.Error("prompt não pede JSON — o parse depende disso")
	}
}

func TestParseFallbackAnswerRespostaNativaAnthropic(t *testing.T) {
	raw := []byte(`{"content":[{"type":"text","text":"{\"answer\":\"B\",\"reasoning\":\"Ulan Bator é a capital.\"}"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana", "B": "Ulan Bator"}}
	got, err := parseFallbackAnswer(raw, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "B" || got.Reasoning == "" {
		t.Errorf("resposta = %+v", got)
	}
}

func TestParseFallbackAnswerJSONComCercaDeCodigo(t *testing.T) {
	// Modelo de texto costuma embrulhar o JSON em ```json ... ```; aceitar isso
	// evita transformar uma resposta boa em erro.
	raw := []byte(`{"content":[{"type":"text","text":"Claro!\n` + "```json" + `\n{\"answer\": \"A\", \"reasoning\": \"porque sim\"}\n` + "```" + `"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana", "B": "Ulan Bator"}}
	got, err := parseFallbackAnswer(raw, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "A" {
		t.Errorf("label = %q", got.Label)
	}
}

func TestParseFallbackAnswerRotuloDesconhecido(t *testing.T) {
	raw := []byte(`{"content":[{"type":"text","text":"{\"answer\":\"Z\"}"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	if _, err := parseFallbackAnswer(raw, p); err == nil {
		t.Error("queria erro para rótulo fora do conjunto")
	}
}
```

- [ ] **Step 2: Rodar os testes e confirmar que falham**

Run: `cd apps/api-go && go test ./... -run TestBuildFallbackPrompt -v`
Expected: FAIL com `undefined: buildFallbackPrompt`

- [ ] **Step 3: Implementar**

```go
package main

// Escalonamento: quando o Jev fica inseguro, a questão vai para um LLM de
// texto pelo adapter `anthropic` do API Router. O prompt pede JSON estrito
// porque a resposta precisa virar uma alternativa, não um parágrafo.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type quizFallbackAnswer struct {
	Label     string `json:"answer"`
	Reasoning string `json:"reasoning"`
}

func buildFallbackPrompt(p quizParsed) string {
	var b strings.Builder
	b.WriteString("Responda a questão de múltipla escolha abaixo.\n\n")
	b.WriteString(p.Question)
	b.WriteString("\n\n")
	for _, label := range p.Order {
		fmt.Fprintf(&b, "%s) %s\n", label, p.Options[label])
	}
	b.WriteString("\nResponda SOMENTE com um objeto JSON no formato ")
	b.WriteString(`{"answer": "<rótulo exatamente como listado acima>", "reasoning": "<uma frase curta>"}`)
	b.WriteString(".\nNão escreva nada fora do JSON.")
	return b.String()
}

// quizJSONRe acha o primeiro objeto JSON do texto, mesmo embrulhado em cerca de
// código ou precedido de conversa fiada.
var quizJSONRe = regexp.MustCompile(`(?s)\{.*\}`)

func parseFallbackAnswer(raw []byte, p quizParsed) (quizFallbackAnswer, error) {
	var native struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &native); err != nil {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: resposta do fallback ilegível: %w", err)
	}
	text := ""
	for _, c := range native.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	if text == "" {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: fallback não devolveu texto")
	}
	match := quizJSONRe.FindString(text)
	if match == "" {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: fallback não devolveu JSON")
	}
	var ans quizFallbackAnswer
	if err := json.Unmarshal([]byte(match), &ans); err != nil {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: JSON do fallback inválido: %w", err)
	}
	if _, ok := p.Options[ans.Label]; !ok {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: fallback devolveu rótulo desconhecido %q", ans.Label)
	}
	return ans, nil
}
```

- [ ] **Step 4: Rodar os testes e confirmar que passam**

Run: `cd apps/api-go && go test ./... -run 'TestBuildFallbackPrompt|TestParseFallbackAnswer' -v`
Expected: PASS

- [ ] **Step 5: Verificação obrigatória e commit**

```bash
cd apps/api-go && gofmt -l . && go vet ./... && go build ./... && go test ./...
git add apps/api-go/quiz_fallback.go apps/api-go/quiz_fallback_test.go
git commit -m "feat(quiz): prompt e parse do fallback Claude"
```

---

### Task 4: Orquestração (Jev → decisão → fallback)

O coração da rota, escrito como função pura com os dois upstreams injetados. Assim a decisão de escalar, a degradação e os orçamentos de tempo são testados sem banco, sem vault e sem rede.

**Files:**
- Create: `apps/api-go/quiz.go`
- Test: `apps/api-go/quiz_test.go`

**Interfaces:**
- Consumes: `parseQuizBlock`, `quizParsed` (Task 1); `buildJevRequest`, `parseJevResponse`, `quizVerdict.margin` (Task 2); `buildFallbackPrompt`, `parseFallbackAnswer` (Task 3).
- Produces:
  ```go
  type quizRequest struct {
      Raw      string            `json:"raw"`
      Question string            `json:"question"`
      Options  map[string]string `json:"options"`
      Explain  bool              `json:"explain"`
  }
  type quizResponse struct {
      Answer        string             `json:"answer"`
      AnswerText    string             `json:"answerText"`
      Confidence    float64            `json:"confidence"`
      Probabilities map[string]float64 `json:"probabilities,omitempty"`
      Source        string             `json:"source"`
      Escalated     bool               `json:"escalated"`
      Degraded      bool               `json:"degraded"`
      Reasoning     string             `json:"reasoning,omitempty"`
      Parsed        quizParsed         `json:"parsed"`
      Timings       quizTimings        `json:"timings"`
  }
  type quizTimings struct {
      JevMs    int64 `json:"jevMs"`
      ClaudeMs int64 `json:"claudeMs"`
      TotalMs  int64 `json:"totalMs"`
  }
  type quizDeps struct {
      jev             func(ctx context.Context, body []byte) ([]byte, error)
      fallback        func(ctx context.Context, prompt string) (string, error)
      minConfidence   float64
      minMargin       float64
  }
  const (
      quizSourceJev    = "jev"
      quizSourceClaude = "claude"
      quizJevBudget      = 8 * time.Second
      quizFallbackBudget = 15 * time.Second
      quizTotalBudget    = 25 * time.Second
      quizMaxBodyLen     = 64 << 10
  )
  var (
      errQuizUpstream = errors.New("quiz: nenhum modelo conseguiu responder")
      errQuizTimeout  = errors.New("quiz: tempo esgotado")
  )
  func answerQuiz(ctx context.Context, req quizRequest, deps quizDeps) (quizResponse, error)
  ```

- [ ] **Step 1: Escrever os testes que falham**

```go
package main

import (
	"context"
	"errors"
	"testing"
)

func depsFake(jevBody string, jevErr error, fbBody string, fbErr error, chamadas *[]string) quizDeps {
	return quizDeps{
		jev: func(ctx context.Context, body []byte) ([]byte, error) {
			*chamadas = append(*chamadas, "jev")
			if jevErr != nil {
				return nil, jevErr
			}
			return []byte(jevBody), nil
		},
		fallback: func(ctx context.Context, prompt string) ([]byte, error) {
			*chamadas = append(*chamadas, "fallback")
			if fbErr != nil {
				return nil, fbErr
			}
			return []byte(fbBody), nil
		},
		minConfidence:   0.75,
		minMargin:       0.15,
	}
}

const (
	jevConfiante = `{"answers":{"resposta":{"type":"choice","choice":"B","confidence":0.93,"probabilities":{"A":0.02,"B":0.93,"C":0.05}}}}`
	jevInseguro  = `{"answers":{"resposta":{"type":"choice","choice":"B","confidence":0.41,"probabilities":{"A":0.39,"B":0.41,"C":0.20}}}}`
	jevEmpatado  = `{"answers":{"resposta":{"type":"choice","choice":"B","confidence":0.90,"probabilities":{"A":0.88,"B":0.90}}}}`
	fbOK         = `{"content":[{"type":"text","text":"{\"answer\":\"A\",\"reasoning\":\"porque sim\"}"}]}`
)

func reqExemplo() quizRequest {
	return quizRequest{Raw: "Qual a capital da Mongólia?\nA) Astana\nB) Ulan Bator\nC) Bishkek"}
}

func TestAnswerQuizJevConfianteNaoEscala(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceJev || got.Escalated {
		t.Errorf("source=%q escalated=%v", got.Source, got.Escalated)
	}
	if got.Answer != "B" || got.AnswerText != "Ulan Bator" {
		t.Errorf("resposta = %q / %q", got.Answer, got.AnswerText)
	}
	if len(chamadas) != 1 || chamadas[0] != "jev" {
		t.Errorf("chamadas = %v, queria só o jev (escalar à toa custa dinheiro e tempo)", chamadas)
	}
}

func TestAnswerQuizConfiancaBaixaEscala(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevInseguro, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceClaude || !got.Escalated {
		t.Errorf("source=%q escalated=%v", got.Source, got.Escalated)
	}
	if got.Answer != "A" || got.Reasoning == "" {
		t.Errorf("resposta = %+v", got)
	}
}

func TestAnswerQuizMargemBaixaEscalaMesmoComConfiancaAlta(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevEmpatado, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if !got.Escalated {
		t.Error("0.88 vs 0.90 é empate técnico — tinha que escalar")
	}
}

func TestAnswerQuizExplainForcaEscalonamento(t *testing.T) {
	var chamadas []string
	req := reqExemplo()
	req.Explain = true
	got, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceClaude {
		t.Errorf("source = %q, queria claude (só ele explica)", got.Source)
	}
}

func TestAnswerQuizJevFalhaVaiDiretoNoFallback(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake("", errors.New("502"), fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceClaude || got.Answer != "A" {
		t.Errorf("resposta = %+v", got)
	}
}

func TestAnswerQuizFallbackFalhaDevolveJevDegradado(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevInseguro, nil, "", errors.New("502"), &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	// No meio de uma questão, um palpite de confiança 0.41 vale mais que uma
	// tela de erro.
	if got.Source != quizSourceJev || !got.Degraded || got.Answer != "B" {
		t.Errorf("resposta = %+v", got)
	}
}

func TestAnswerQuizAmbosFalham(t *testing.T) {
	var chamadas []string
	_, err := answerQuiz(context.Background(), reqExemplo(), depsFake("", errors.New("x"), "", errors.New("y"), &chamadas))
	if !errors.Is(err, errQuizUpstream) {
		t.Errorf("err = %v, queria errQuizUpstream", err)
	}
}

func TestAnswerQuizAceitaAlternativasJaSeparadas(t *testing.T) {
	var chamadas []string
	req := quizRequest{
		Question: "Qual a capital da Mongólia?",
		Options:  map[string]string{"A": "Astana", "B": "Ulan Bator", "C": "Bishkek"},
	}
	got, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Parsed.Question != req.Question || len(got.Parsed.Options) != 3 {
		t.Errorf("parsed = %+v", got.Parsed)
	}
	if len(got.Parsed.Order) != 3 || got.Parsed.Order[0] != "A" {
		t.Errorf("ordem = %v, queria rótulos ordenados (mapa em Go não tem ordem)", got.Parsed.Order)
	}
}

func TestAnswerQuizBlocoImpossivelDeSeparar(t *testing.T) {
	var chamadas []string
	_, err := answerQuiz(context.Background(), quizRequest{Raw: "texto solto"}, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if !errors.Is(err, errQuizUnparseable) {
		t.Errorf("err = %v, queria errQuizUnparseable", err)
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma (não gastar API com lixo)", chamadas)
	}
}
```

- [ ] **Step 2: Rodar os testes e confirmar que falham**

Run: `cd apps/api-go && go test ./... -run TestAnswerQuiz -v`
Expected: FAIL com `undefined: answerQuiz`

- [ ] **Step 3: Implementar**

```go
package main

// Orquestração da resposta de questão: Jev primeiro (barato e rápido), LLM de
// texto só quando o Jev demonstra insegurança. Os dois upstreams entram por
// injeção pra esta lógica — que é onde moram as decisões — ser testável sem
// banco, sem vault e sem rede.

import (
	"context"
	"errors"
	"sort"
	"time"
)

const (
	quizSourceJev    = "jev"
	quizSourceClaude = "claude"

	// Orçamentos próprios: o API Router tem tetos largos demais pra uso
	// interativo (30s por tentativa, 60s de rotação — ver apirouter.go). O ctx
	// chega até o request do provider, então o menor prevalece. O deadline
	// curto do Jev existe pra sobrar tempo de escalar: um Jev lento não pode
	// consumir o orçamento que o fallback vai precisar.
	quizJevBudget      = 8 * time.Second
	quizFallbackBudget = 15 * time.Second
	quizTotalBudget    = 25 * time.Second

	quizMaxBodyLen = 64 << 10
)

var (
	errQuizUpstream = errors.New("quiz: nenhum modelo conseguiu responder")
	errQuizTimeout  = errors.New("quiz: tempo esgotado")
)

type quizRequest struct {
	Raw      string            `json:"raw"`
	Question string            `json:"question"`
	Options  map[string]string `json:"options"`
	Explain  bool              `json:"explain"`
}

type quizTimings struct {
	JevMs    int64 `json:"jevMs"`
	ClaudeMs int64 `json:"claudeMs"`
	TotalMs  int64 `json:"totalMs"`
}

type quizResponse struct {
	Answer        string             `json:"answer"`
	AnswerText    string             `json:"answerText"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Source        string             `json:"source"`
	Escalated     bool               `json:"escalated"`
	Degraded      bool               `json:"degraded"`
	Reasoning     string             `json:"reasoning,omitempty"`
	Parsed        quizParsed         `json:"parsed"`
	Timings       quizTimings        `json:"timings"`
}

type quizDeps struct {
	jev      func(ctx context.Context, body []byte) ([]byte, error)
	// fallback pede texto ao agent-go (Claude Code em container) — não ao
	// API Router: não há chave de API envolvida.
	fallback      func(ctx context.Context, prompt string) (string, error)
	minConfidence float64
	minMargin     float64
}

func answerQuiz(ctx context.Context, req quizRequest, deps quizDeps) (quizResponse, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, quizTotalBudget)
	defer cancel()

	parsed, err := resolveQuizParsed(req)
	if err != nil {
		return quizResponse{}, err
	}

	resp := quizResponse{Parsed: parsed}
	verdict, jevMs, jevErr := askJev(ctx, parsed, deps)
	resp.Timings.JevMs = jevMs

	escalate := req.Explain || jevErr != nil ||
		verdict.Confidence < deps.minConfidence ||
		verdict.margin() < deps.minMargin

	if !escalate {
		fillFromJev(&resp, verdict, parsed)
		resp.Timings.TotalMs = time.Since(started).Milliseconds()
		return resp, nil
	}

	ans, claudeMs, fbErr := askFallback(ctx, parsed, deps)
	resp.Timings.ClaudeMs = claudeMs
	switch {
	case fbErr == nil:
		resp.Source = quizSourceClaude
		resp.Escalated = true
		resp.Answer = ans.Label
		resp.AnswerText = parsed.Options[ans.Label]
		resp.Reasoning = ans.Reasoning
		resp.Confidence = verdict.Confidence
		resp.Probabilities = verdict.Probabilities
	case jevErr == nil:
		// Degradação: o fallback morreu, mas o palpite do Jev existe. Devolver
		// palpite fraco é melhor que devolver erro no meio de uma questão.
		fillFromJev(&resp, verdict, parsed)
		resp.Degraded = true
	default:
		if ctx.Err() != nil {
			return quizResponse{}, errQuizTimeout
		}
		return quizResponse{}, errQuizUpstream
	}
	resp.Timings.TotalMs = time.Since(started).Milliseconds()
	return resp, nil
}

func fillFromJev(resp *quizResponse, v quizVerdict, p quizParsed) {
	resp.Source = quizSourceJev
	resp.Answer = v.Label
	resp.AnswerText = p.Options[v.Label]
	resp.Confidence = v.Confidence
	resp.Probabilities = v.Probabilities
}

// resolveQuizParsed aceita os dois formatos de entrada: bloco cru (o caminho
// normal) ou alternativas já separadas pelo cliente.
func resolveQuizParsed(req quizRequest) (quizParsed, error) {
	if len(req.Options) > 0 {
		order := make([]string, 0, len(req.Options))
		for label := range req.Options {
			order = append(order, label)
		}
		// Mapa em Go não tem ordem: sem ordenar, o prompt do fallback sairia
		// embaralhado a cada requisição e a resposta mudaria sozinha.
		sort.Strings(order)
		return quizParsed{Question: req.Question, Options: req.Options, Order: order}, nil
	}
	return parseQuizBlock(req.Raw)
}

func askJev(ctx context.Context, p quizParsed, deps quizDeps) (quizVerdict, int64, error) {
	body, err := buildJevRequest(p)
	if err != nil {
		return quizVerdict{}, 0, err
	}
	jevCtx, cancel := context.WithTimeout(ctx, quizJevBudget)
	defer cancel()
	started := time.Now()
	raw, err := deps.jev(jevCtx, body)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return quizVerdict{}, elapsed, err
	}
	v, err := parseJevResponse(raw, p)
	return v, elapsed, err
}

func askFallback(ctx context.Context, p quizParsed, deps quizDeps) (quizFallbackAnswer, int64, error) {
	fbCtx, cancel := context.WithTimeout(ctx, quizFallbackBudget)
	defer cancel()
	started := time.Now()
	texto, err := deps.fallback(fbCtx, buildFallbackPrompt(p))
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return quizFallbackAnswer{}, elapsed, err
	}
	ans, err := parseFallbackAnswer(texto, p)
	return ans, elapsed, err
}
```

- [ ] **Step 4: Rodar os testes e confirmar que passam**

Run: `cd apps/api-go && go test ./... -run TestAnswerQuiz -v`
Expected: PASS nos 9 testes

- [ ] **Step 5: Verificação obrigatória e commit**

```bash
cd apps/api-go && gofmt -l . && go vet ./... && go build ./... && go test ./...
git add apps/api-go/quiz.go apps/api-go/quiz_test.go
git commit -m "feat(quiz): orquestração Jev com escalonamento e degradação"
```

---

### Task 5: Configuração, handler HTTP e rota

Liga a orquestração ao API Router e expõe a rota. Inclui o ajuste em `isNativeClient` — sem ele a extensão loga e fica sem token, permanentemente.

**Files:**
- Modify: `apps/api-go/config.go` (struct `Config` + `loadConfig`)
- Modify: `apps/api-go/handlers_auth.go:42` (`isNativeClient`)
- Modify: `apps/api-go/routes.go` (registro da rota)
- Create: `apps/api-go/handlers_quiz.go`
- Test: `apps/api-go/handlers_quiz_test.go`, `apps/api-go/config_test.go` (acrescentar)

**Interfaces:**
- Consumes: `answerQuiz`, `quizRequest`, `quizDeps`, `errQuizUpstream`, `errQuizTimeout`, `quizMaxBodyLen` (Task 4); `errQuizUnparseable` (Task 1); `quizJevPath` (Task 2); `buildChatRequest` (existente, `apirouter_adapters.go:33`); `executeAPIRouterRequest` (existente, `apirouter.go:265`).
- Produces: `POST /quiz/answer`; `Config.QuizJevProviderID`, `Config.QuizFallbackProviderID`, `Config.QuizMinConfidence`, `Config.QuizMinMargin`.

- [ ] **Step 1: Escrever o teste que falha (mapeamento de erro → HTTP, e isNativeClient)**

```go
package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuizErrStatus(t *testing.T) {
	casos := []struct {
		err   error
		code  string
		http_ int
	}{
		{errQuizUnparseable, "UNPARSEABLE", http.StatusUnprocessableEntity},
		{errQuizTimeout, "UPSTREAM_TIMEOUT", http.StatusGatewayTimeout},
		{errQuizUpstream, "UPSTREAM_FAILED", http.StatusBadGateway},
		{errAPIRouterNoActiveKeys, "NO_ACTIVE_KEYS", http.StatusServiceUnavailable},
		{errors.New("qualquer outra"), "UPSTREAM_FAILED", http.StatusBadGateway},
	}
	for _, c := range casos {
		ae := quizErr(c.err)
		if ae.Code != c.code || ae.Status != c.http_ {
			t.Errorf("quizErr(%v) = %s/%d, queria %s/%d", c.err, ae.Code, ae.Status, c.code, c.http_)
		}
	}
}

func TestIsNativeClientAceitaExtensaoFirefox(t *testing.T) {
	// Fetch de extensão Firefox SEMPRE manda Origin: moz-extension://<uuid>.
	// Sem isto o login devolve só cookies httpOnly SameSite=Lax, que a
	// extensão não consegue ler nem reenviar: logada e sem token, pra sempre.
	r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	r.Header.Set("Origin", "moz-extension://8f3a1c2e-0000-4000-8000-abcdefabcdef")
	if !isNativeClient(r) {
		t.Error("extensão Firefox precisa receber os tokens no corpo")
	}
}

func TestIsNativeClientRecusaOrigemWeb(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	r.Header.Set("Origin", "https://santos-tech.com")
	if isNativeClient(r) {
		t.Error("origem web continua no fluxo de cookie")
	}
}

func TestIsNativeClientSemOrigem(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	if !isNativeClient(r) {
		t.Error("cliente nativo (sem Origin) continua valendo")
	}
}
```

- [ ] **Step 2: Rodar os testes e confirmar que falham**

Run: `cd apps/api-go && go test ./... -run 'TestQuizErrStatus|TestIsNativeClient' -v`
Expected: FAIL com `undefined: quizErr` e falha em `TestIsNativeClientAceitaExtensaoFirefox`

- [ ] **Step 3: Ajustar `isNativeClient`**

Em `handlers_auth.go:42`, substituir:

```go
func isNativeClient(r *http.Request) bool {
	// Origin vazio = app nativo sem cookie jar. `moz-extension://` = a extensão
	// do quiz: o navegador preenche esse header e uma página web não consegue
	// forjá-lo, então aceitar o prefixo não amplia quem pode autenticar — só
	// muda o formato da resposta pra um cliente que já provou a senha.
	o := r.Header.Get("Origin")
	return o == "" || strings.HasPrefix(o, "moz-extension://")
}
```

- [ ] **Step 4: Acrescentar a configuração**

Em `config.go`, dentro da struct `Config`:

```go
	// Rota POST /quiz/answer (extensão de questões). Provider por ID e não por
	// nome: `name` é editável na UI admin, e renomear quebraria a extensão em
	// silêncio. Zero = rota responde 503.
	QuizJevProviderID      int64
	QuizFallbackProviderID int64
	QuizMinConfidence      float64 // abaixo disso, escala pro fallback
	QuizMinMargin          float64 // p1−p2 abaixo disso = empate técnico, escala
```

E em `loadConfig`, junto dos demais campos opcionais:

```go
	c.QuizJevProviderID = getEnvInt64("QUIZ_JEV_PROVIDER_ID", 0)
	c.QuizFallbackProviderID = getEnvInt64("QUIZ_FALLBACK_PROVIDER_ID", 0)
	c.QuizMinConfidence = getEnvFloat("QUIZ_MIN_CONFIDENCE", 0.75)
	c.QuizMinMargin = getEnvFloat("QUIZ_MIN_MARGIN", 0.15)
```

Com os helpers, ao lado de `getEnvDuration` (`config.go:338`):

```go
func getEnvInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
```

Acrescentar `"strconv"` aos imports de `config.go`.

- [ ] **Step 5: Escrever o handler**

Criar `apps/api-go/handlers_quiz.go`:

```go
package main

// POST /quiz/answer — responde uma questão de múltipla escolha selecionada na
// página, para a extensão de navegador. Rota dedicada e estreita de propósito:
// a extensão NÃO alcança /auth/admin/api-router/.../proxy, que permitiria
// disparar qualquer requisição contra qualquer provider com as chaves da
// empresa.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/santos-tech/auth/db"
)

func quizErr(err error) *AppError {
	switch {
	case errors.Is(err, errQuizUnparseable):
		return appErr(http.StatusUnprocessableEntity, "UNPARSEABLE",
			"Não consegui separar as alternativas — selecione o enunciado e as alternativas")
	case errors.Is(err, errQuizTimeout):
		return appErr(http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT", "Tempo esgotado ao consultar os modelos")
	case errors.Is(err, errAPIRouterNoActiveKeys):
		return appErr(http.StatusServiceUnavailable, "NO_ACTIVE_KEYS", "Provider sem chaves ativas")
	default:
		return appErr(http.StatusBadGateway, "UPSTREAM_FAILED", "Nenhum modelo conseguiu responder")
	}
}

func (s *Server) handleQuizAnswer(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	if s.cfg.QuizJevProviderID == 0 || s.cfg.QuizFallbackProviderID == 0 {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED",
			"QUIZ_JEV_PROVIDER_ID/QUIZ_FALLBACK_PROVIDER_ID não configurados"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, quizMaxBodyLen)
	var body quizRequest
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "corpo inválido"))
		return
	}
	if body.Raw == "" && len(body.Options) == 0 {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "informe `raw` ou `options`"))
		return
	}

	jevProvider, err := s.q.GetAPIRouterProvider(r.Context(), s.cfg.QuizJevProviderID)
	if err != nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED", "provider do Jev não encontrado"))
		return
	}
	fbProvider, err := s.q.GetAPIRouterProvider(r.Context(), s.cfg.QuizFallbackProviderID)
	if err != nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED", "provider de fallback não encontrado"))
		return
	}

	resp, err := answerQuiz(r.Context(), body, quizDeps{
		jev:             s.quizJevCaller(jevProvider),
		fallback:        s.quizFallbackCaller(fbProvider),
		fallbackAdapter: fbProvider.ChatAdapter,
		minConfidence:   s.cfg.QuizMinConfidence,
		minMargin:       s.cfg.QuizMinMargin,
	})
	if err != nil {
		writeErr(w, quizErr(err))
		return
	}
	// Sem o enunciado no log: é conteúdo do usuário e não ajuda a operar.
	slog.Info("quiz: resposta",
		"source", resp.Source, "escalated", resp.Escalated, "degraded", resp.Degraded,
		"confidence", resp.Confidence, "total_ms", resp.Timings.TotalMs)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) quizJevCaller(provider db.ApiRouterProvider) func(context.Context, []byte) ([]byte, error) {
	return func(ctx context.Context, body []byte) ([]byte, error) {
		out, err := s.executeAPIRouterRequest(ctx, provider, http.MethodPost, quizJevPath, body, "", nil)
		if err != nil {
			return nil, err
		}
		if out.StatusCode >= 300 {
			return nil, fmt.Errorf("quiz: jev respondeu %d", out.StatusCode)
		}
		return out.Body, nil
	}
}

func (s *Server) quizFallbackCaller(provider db.ApiRouterProvider) func(context.Context, string) ([]byte, error) {
	return func(ctx context.Context, prompt string) ([]byte, error) {
		path, reqBody, headers, err := buildChatRequest(provider.ChatAdapter, provider.ChatModel, prompt)
		if err != nil {
			return nil, err
		}
		// chat_path do provider vence o default do adapter — mesmo critério do
		// handleAPIRouterChat (handlers_apirouter.go:636).
		if provider.ChatPath != "" {
			path = provider.ChatPath
		}
		out, err := s.executeAPIRouterRequest(ctx, provider, http.MethodPost, path, reqBody, "", headers)
		if err != nil {
			return nil, err
		}
		if out.StatusCode >= 300 {
			return nil, fmt.Errorf("quiz: fallback respondeu %d", out.StatusCode)
		}
		return out.Body, nil
	}
}
```

- [ ] **Step 6: Registrar a rota**

Em `routes.go`, perto do bloco do API Router (linha ~85):

```go
	// Extensão de questões: rota estreita, authGuard (qualquer usuário logado),
	// não adminGuard — ver handlers_quiz.go.
	mux.HandleFunc("POST /quiz/answer", s.rateLimit(30, min, s.authGuard(s.handleQuizAnswer)))
```

- [ ] **Step 7: Rodar os testes e confirmar que passam**

Run: `cd apps/api-go && go test ./... -run 'TestQuizErrStatus|TestIsNativeClient' -v`
Expected: PASS

- [ ] **Step 8: Verificação obrigatória e commit**

```bash
cd apps/api-go && gofmt -l . && go vet ./... && go build ./... && go test ./...
git add apps/api-go/handlers_quiz.go apps/api-go/handlers_quiz_test.go apps/api-go/config.go apps/api-go/routes.go apps/api-go/handlers_auth.go
git commit -m "feat(quiz): rota POST /quiz/answer e suporte a cliente de extensão"
```

---

### Task 6: Cadastro do provider Jev e deploy do backend

A rota não serve pra nada sem o provider cadastrado. Esta task fecha o backend e o deixa respondendo em produção, verificado por fora.

**Files:**
- Nenhum arquivo de código. Configuração no Coolify e cadastro pela UI admin.

**Interfaces:**
- Consumes: rota `POST /quiz/answer` (Task 5).
- Produces: `QUIZ_JEV_PROVIDER_ID` e `QUIZ_FALLBACK_PROVIDER_ID` com valores reais; backend em produção respondendo.

- [ ] **Step 1: Cadastrar o provider Jev pela UI admin do API Router**

Em `/auth/admin` → API Router → novo provider:

| Campo | Valor |
|---|---|
| `name` | `jev` |
| `base_url` | `https://api.typesafe.ai` |
| `auth_header` | `Authorization` |
| `auth_scheme` | `Bearer` |
| `unauthorized_codes` | `401` |
| `no_credit_codes` | `402,429` |
| `test_path` / `test_method` | `/v1/systemone` / `POST` |

A chave do Jev entra pela tela de chaves do provider, que já a cifra no banco. **Não escrever a chave em arquivo nenhum.**

- [ ] **Step 2: Anotar os IDs**

O ID do provider Jev aparece na listagem. O provider de fallback (Anthropic) já deve existir — se não existir, cadastrar com `chat_adapter = anthropic`, `base_url = https://api.anthropic.com`, `chat_model = claude-sonnet-4-5`.

- [ ] **Step 3: Configurar o env no Coolify e reimplantar**

No serviço do `api-go` (Coolify no Contabo — `ssh contabo`, credenciais na memória `reference_coolify_santos_tech_api`):

```
QUIZ_JEV_PROVIDER_ID=<id do jev>
QUIZ_FALLBACK_PROVIDER_ID=<id do anthropic>
QUIZ_MIN_CONFIDENCE=<valor calibrado na Task 0>
QUIZ_MIN_MARGIN=<valor calibrado na Task 0>
```

- [ ] **Step 4: Fazer push da branch e deploy**

```bash
cd /home/guilherme/Projetos/santos-tech-infra
gofmt -l apps/api-go && (cd apps/api-go && go vet ./... && go build ./... && go test ./...)
git push -u origin feat/quiz-jev-extension
```

Disparar o deploy no Coolify e **esperar um tempo fixo** (preferência do usuário: `ScheduleWakeup`, não laço de polling).

- [ ] **Step 5: Smoke test contra produção**

Com um access token válido de uma sessão de teste (obtido por `POST /auth/login` sem header `Origin`):

```bash
curl -sS -X POST https://api.santos-tech.com/quiz/answer \
  -H "Authorization: Bearer $ACCESS" -H "Content-Type: application/json" \
  -d '{"raw":"Qual a capital da Mongólia?\nA) Astana\nB) Ulan Bator\nC) Bishkek"}' | jq .
```

Expected: `answer` = `"B"`, `source` presente, `parsed.options` com 3 entradas.

Verificar também os erros: corpo `{"raw":"texto solto"}` deve devolver 422 `UNPARSEABLE`, e sem `Authorization` deve devolver 401.

---

### Task 7: Extensão — manifest e background (sessão e chamada)

**Files:**
- Create: `apps/quiz-extension/manifest.json`
- Create: `apps/quiz-extension/src/background.js`
- Create: `apps/quiz-extension/README.md`

**Interfaces:**
- Consumes: `POST /quiz/answer`, `POST /auth/login`, `POST /auth/refresh` (Tasks 5 e 6).
- Produces: mensagens de runtime que os outros arquivos usam —
  `{type: "ask", raw}` → `{ok: true, data: quizResponse}` ou `{ok: false, error: string}`;
  `{type: "login", identifier, password}` → `{ok: true}` ou `{ok: false, error}`;
  `{type: "status"}` → `{loggedIn: boolean}`.

**Divergência consciente do spec:** o spec lista `src/overlay.css` como arquivo separado. O CSS vai embutido no `content.js` (Task 8) porque um arquivo separado precisaria entrar em `web_accessible_resources`, ficando legível por qualquer página visitada. Um arquivo a menos e uma exposição a menos.

- [ ] **Step 1: Criar o manifest**

```json
{
  "manifest_version": 3,
  "name": "Quiz Jev",
  "version": "0.1.0",
  "description": "Responde a questão de múltipla escolha selecionada na página.",
  "browser_specific_settings": {
    "gecko": { "id": "quiz-jev@santos-tech.com", "strict_min_version": "128.0" }
  },
  "permissions": ["storage", "activeTab", "scripting"],
  "host_permissions": ["https://api.santos-tech.com/*"],
  "background": { "scripts": ["src/background.js"] },
  "options_ui": { "page": "src/options.html", "open_in_tab": true },
  "commands": {
    "answer-selection": {
      "suggested_key": { "default": "Alt+Q" },
      "description": "Responder a questão selecionada"
    }
  }
}
```

Sem `content_scripts` e sem `<all_urls>`: o script é injetado sob demanda, e a extensão não lê nenhuma página enquanto o atalho não for pressionado.

- [ ] **Step 2: Escrever o background**

```js
// Background da extensão: guarda a sessão, fala com a api-go e dispara a
// captura quando o atalho é pressionado. Nenhuma credencial de API passa por
// aqui — a extensão só conhece o próprio login do usuário.

const API = "https://api.santos-tech.com";

async function getTokens() {
  const { tokens } = await browser.storage.local.get("tokens");
  return tokens || null;
}

async function setTokens(tokens) {
  await browser.storage.local.set({ tokens });
}

// Fila de refresh: o refresh token é rotativo e fail-closed — reusar um token
// já rotacionado faz o servidor revogar TODAS as sessões do usuário. Dois
// refresh concorrentes causariam exatamente isso, então só existe um em voo.
let refreshInFlight = null;

async function refreshTokens() {
  if (refreshInFlight) return refreshInFlight;
  refreshInFlight = (async () => {
    const tokens = await getTokens();
    if (!tokens?.refreshToken) throw new Error("sem sessão");
    const res = await fetch(`${API}/auth/refresh`, {
      method: "POST",
      headers: { Authorization: `Bearer ${tokens.refreshToken}` },
    });
    if (!res.ok) {
      await browser.storage.local.remove("tokens");
      throw new Error("sessão expirada");
    }
    const data = await res.json();
    // Gravar o par novo ANTES de qualquer outra coisa: se o processo morrer
    // aqui, o token antigo já não vale e o novo se perdeu.
    await setTokens({ accessToken: data.accessToken, refreshToken: data.refreshToken });
    return data.accessToken;
  })();
  try {
    return await refreshInFlight;
  } finally {
    refreshInFlight = null;
  }
}

async function apiFetch(path, body, { retried = false } = {}) {
  const tokens = await getTokens();
  if (!tokens?.accessToken) throw new Error("Faça login nas opções da extensão");
  const res = await fetch(`${API}${path}`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${tokens.accessToken}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
  if (res.status === 401 && !retried) {
    await refreshTokens();
    return apiFetch(path, body, { retried: true });
  }
  const data = await res.json().catch(() => ({}));
  // writeErr serializa {"code","message"} no topo (errors.go:32) — não aninhado.
  if (!res.ok) throw new Error(data?.message || `erro ${res.status}`);
  return data;
}

async function login(identifier, password) {
  const res = await fetch(`${API}/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ identifier, password }),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data?.message || `erro ${res.status}`);
  if (data.mfaRequired) {
    throw new Error("Sua conta pede 2FA e a extensão ainda não trata isso.");
  }
  if (!data.accessToken) {
    // Acontece se o servidor não reconhecer a origem moz-extension:// como
    // cliente nativo (ver isNativeClient no api-go): login funciona, tokens
    // não vêm, e a extensão ficaria logada e inútil.
    throw new Error("Servidor não devolveu tokens — api-go precisa do ajuste em isNativeClient.");
  }
  await setTokens({ accessToken: data.accessToken, refreshToken: data.refreshToken });
}

browser.commands.onCommand.addListener(async (command) => {
  if (command !== "answer-selection") return;
  const [tab] = await browser.tabs.query({ active: true, currentWindow: true });
  if (!tab?.id) return;
  try {
    await browser.scripting.executeScript({ target: { tabId: tab.id }, files: ["src/content.js"] });
    await browser.tabs.sendMessage(tab.id, { type: "start" });
  } catch (e) {
    console.error("quiz-jev: não consegui injetar na aba", e);
  }
});

browser.runtime.onMessage.addListener((msg) => {
  if (msg?.type === "ask") {
    return apiFetch("/quiz/answer", { raw: msg.raw, explain: !!msg.explain })
      .then((data) => ({ ok: true, data }))
      .catch((e) => ({ ok: false, error: e.message }));
  }
  if (msg?.type === "login") {
    return login(msg.identifier, msg.password)
      .then(() => ({ ok: true }))
      .catch((e) => ({ ok: false, error: e.message }));
  }
  if (msg?.type === "status") {
    return getTokens().then((t) => ({ loggedIn: !!t?.accessToken }));
  }
  return false;
});
```

- [ ] **Step 3: Escrever o README da extensão**

```markdown
# Quiz Jev — extensão

Seleciona a questão na página, `Alt+Q`, e a resposta aparece num overlay.

## Instalar (Zen / Firefox)

1. `about:debugging#/runtime/this-firefox`
2. "Carregar extensão temporária…" → escolher `manifest.json` desta pasta.
3. Abrir as opções da extensão e fazer login com a conta santos-tech.

Extensão temporária some ao fechar o navegador; recarregar pelo mesmo caminho.

## Arquitetura

A extensão não conhece Jev nem Claude: ela manda o texto selecionado para
`POST /quiz/answer` da `api-go`, que decide qual modelo responde. Nenhuma chave
de API vive aqui.
```

- [ ] **Step 4: Verificar que o manifest é válido**

Carregar em `about:debugging#/runtime/this-firefox` e confirmar que aparece sem erro. O console do background (botão "Inspecionar") deve abrir limpo.

- [ ] **Step 5: Commit**

```bash
git add apps/quiz-extension/manifest.json apps/quiz-extension/src/background.js apps/quiz-extension/README.md
git commit -m "feat(quiz-extension): manifest e background com sessão santos-tech"
```

---

### Task 8: Extensão — captura e overlay

**Files:**
- Create: `apps/quiz-extension/src/content.js`

**Interfaces:**
- Consumes: mensagens `{type: "start"}` do background; responde a `{type: "ask"}` com o `quizResponse` da Task 4.
- Produces: nada consumido por outras tasks.

- [ ] **Step 1: Escrever o content script**

```js
// Injetado sob demanda pelo background (nunca declarativo): lê a seleção,
// desenha o overlay e mostra a resposta. Não fala com a API — quem faz isso é
// o background, que é quem tem os tokens.

if (!window.__quizJevCarregado) {
  window.__quizJevCarregado = true;

  const ID = "__quiz-jev-overlay";
  // Shadow DOM + `all: initial`: sem isso o CSS da página deforma o card, e
  // site de prova costuma ter CSS agressivo.
  const CSS = `
    :host { all: initial; }
    .card {
      position: fixed; z-index: 2147483647; max-width: 340px;
      font: 14px/1.45 system-ui, sans-serif; color: #111;
      background: #fff; border: 1px solid #d4d4d8; border-radius: 10px;
      box-shadow: 0 8px 28px rgba(0,0,0,.18); padding: 12px 14px;
    }
    .linha { display: flex; align-items: baseline; gap: 8px; }
    .letra { font-size: 28px; font-weight: 700; line-height: 1; }
    .texto { flex: 1; }
    .badge { font-size: 11px; text-transform: uppercase; letter-spacing: .04em;
             padding: 2px 6px; border-radius: 999px; background: #e4e4e7; }
    .badge.claude { background: #ddd6fe; }
    .aviso { margin-top: 8px; font-size: 12px; color: #92400e; }
    .motivo { margin-top: 8px; font-size: 13px; color: #3f3f46; }
    .barras { margin-top: 10px; display: grid; gap: 3px; }
    .barra { display: grid; grid-template-columns: 18px 1fr 38px; gap: 6px;
             align-items: center; font-size: 12px; color: #52525b; }
    .barra i { display: block; height: 6px; border-radius: 3px; background: #a1a1aa; }
    .barra.escolhida i { background: #2563eb; }
    .erro { color: #b91c1c; }
    @media (prefers-color-scheme: dark) {
      .card { background: #18181b; color: #fafafa; border-color: #3f3f46; }
      .badge { background: #3f3f46; } .motivo { color: #d4d4d8; }
    }
  `;

  function fechar() {
    document.getElementById(ID)?.remove();
    document.removeEventListener("keydown", aoTeclar, true);
    document.removeEventListener("mousedown", aoClicar, true);
  }

  function aoTeclar(e) {
    if (e.key === "Escape") fechar();
  }

  function aoClicar(e) {
    const host = document.getElementById(ID);
    if (host && !e.composedPath().includes(host)) fechar();
  }

  function abrir(rect) {
    fechar();
    const host = document.createElement("div");
    host.id = ID;
    const shadow = host.attachShadow({ mode: "open" });
    const style = document.createElement("style");
    style.textContent = CSS;
    const card = document.createElement("div");
    card.className = "card";
    // Clamp pro card não sair da tela quando a seleção está no rodapé/borda.
    const topo = Math.min(rect.bottom + 8, window.innerHeight - 180);
    const esq = Math.min(rect.left, window.innerWidth - 360);
    card.style.top = `${Math.max(8, topo)}px`;
    card.style.left = `${Math.max(8, esq)}px`;
    shadow.append(style, card);
    document.body.appendChild(host);
    document.addEventListener("keydown", aoTeclar, true);
    document.addEventListener("mousedown", aoClicar, true);
    return card;
  }

  function barras(probs, escolhida) {
    if (!probs) return "";
    const itens = Object.entries(probs).sort((a, b) => b[1] - a[1]);
    return `<div class="barras">${itens.map(([label, p]) => `
      <div class="barra ${label === escolhida ? "escolhida" : ""}">
        <span>${label}</span><i style="width:${Math.round(p * 100)}%"></i>
        <span>${Math.round(p * 100)}%</span>
      </div>`).join("")}</div>`;
  }

  function mostrarResposta(card, d) {
    const badge = d.source === "claude" ? "claude" : "jev";
    card.innerHTML = `
      <div class="linha">
        <span class="letra">${d.answer}</span>
        <span class="texto">${d.answerText || ""}</span>
        <span class="badge ${badge}">${badge}</span>
      </div>
      ${d.degraded ? `<div class="aviso">Confiança baixa — o segundo modelo não respondeu.</div>` : ""}
      ${d.reasoning ? `<div class="motivo">${d.reasoning}</div>` : ""}
      ${barras(d.probabilities, d.answer)}
    `;
  }

  browser.runtime.onMessage.addListener(async (msg) => {
    if (msg?.type !== "start") return;
    const sel = window.getSelection();
    const raw = sel ? sel.toString().trim() : "";
    if (!raw) return; // sem seleção não chama a API
    const rect = sel.getRangeAt(0).getBoundingClientRect();
    const card = abrir(rect);
    card.textContent = "Consultando…";
    const resp = await browser.runtime.sendMessage({ type: "ask", raw });
    if (!document.getElementById(ID)) return; // usuário fechou enquanto carregava
    if (resp?.ok) mostrarResposta(card, resp.data);
    else card.innerHTML = `<div class="erro">${resp?.error || "falhou"}</div>`;
  });
}
```

- [ ] **Step 2: Testar manualmente o caminho feliz**

Recarregar a extensão, abrir qualquer página, selecionar um bloco de questão, `Alt+Q`. Esperado: card com a letra, o texto e a barra de probabilidades.

- [ ] **Step 3: Testar os caminhos ruins**

| Caso | Esperado |
|---|---|
| Sem seleção + `Alt+Q` | nada acontece, nenhuma requisição na aba de rede |
| Selecionar texto solto sem alternativas | card com "selecione o enunciado e as alternativas" |
| Esc com o card aberto | fecha |
| Clicar fora | fecha |
| Selecionar no rodapé da página | card visível, dentro da tela |

- [ ] **Step 4: Confirmar que `activeTab` basta**

Este é o risco registrado no spec. Se a injeção falhar com erro de permissão no console do background, o plano B é acrescentar `"host_permissions": ["<all_urls>"]` ao manifest e recarregar. Anotar no README qual dos dois valeu.

- [ ] **Step 5: Commit**

```bash
git add apps/quiz-extension/src/content.js
git commit -m "feat(quiz-extension): captura da seleção e overlay em shadow DOM"
```

---

### Task 9: Extensão — tela de opções (login)

**Files:**
- Create: `apps/quiz-extension/src/options.html`
- Create: `apps/quiz-extension/src/options.js`

**Interfaces:**
- Consumes: mensagens `{type: "login"}` e `{type: "status"}` do background (Task 7).
- Produces: nada consumido por outras tasks.

- [ ] **Step 1: Escrever a página**

```html
<!DOCTYPE html>
<meta charset="utf-8">
<title>Quiz Jev — opções</title>
<style>
  body { font: 15px/1.5 system-ui, sans-serif; max-width: 380px; margin: 32px auto; padding: 0 16px; }
  label { display: block; margin: 12px 0 4px; font-size: 13px; color: #52525b; }
  input { width: 100%; padding: 8px; border: 1px solid #d4d4d8; border-radius: 6px; font-size: 14px; }
  button { margin-top: 16px; padding: 9px 16px; border: 0; border-radius: 6px;
           background: #2563eb; color: #fff; font-size: 14px; cursor: pointer; }
  #status { margin-top: 14px; font-size: 13px; }
  .ok { color: #15803d; } .erro { color: #b91c1c; }
</style>

<h1>Quiz Jev</h1>
<p id="sessao">…</p>

<form id="form">
  <label for="identifier">E-mail ou usuário</label>
  <input id="identifier" name="identifier" autocomplete="username" required>
  <label for="password">Senha</label>
  <input id="password" name="password" type="password" autocomplete="current-password" required>
  <button type="submit">Entrar</button>
</form>

<p id="status"></p>
<p style="margin-top:24px;font-size:13px;color:#71717a">
  Atalho: <code>Alt+Q</code> com a questão selecionada. Para mudar, use
  <code>about:addons</code> → engrenagem → "Gerenciar atalhos de extensões".
</p>

<script src="options.js"></script>
```

- [ ] **Step 2: Escrever o script**

```js
const status = document.getElementById("status");
const sessao = document.getElementById("sessao");

async function atualizarSessao() {
  const { loggedIn } = await browser.runtime.sendMessage({ type: "status" });
  sessao.textContent = loggedIn ? "Sessão ativa." : "Sem sessão — faça login.";
}

document.getElementById("form").addEventListener("submit", async (e) => {
  e.preventDefault();
  status.textContent = "Entrando…";
  status.className = "";
  const identifier = document.getElementById("identifier").value;
  const password = document.getElementById("password").value;
  const resp = await browser.runtime.sendMessage({ type: "login", identifier, password });
  if (resp?.ok) {
    status.textContent = "Pronto.";
    status.className = "ok";
    document.getElementById("password").value = "";
  } else {
    status.textContent = resp?.error || "falhou";
    status.className = "erro";
  }
  atualizarSessao();
});

atualizarSessao();
```

- [ ] **Step 3: Testar o login**

Abrir as opções, entrar com a conta santos-tech. Esperado: "Pronto." e "Sessão ativa.". Se aparecer "Servidor não devolveu tokens", o ajuste do `isNativeClient` (Task 5) não chegou em produção — verificar o deploy.

- [ ] **Step 4: Testar a renovação de sessão**

Em `about:debugging` → console do background:

```js
browser.storage.local.get("tokens").then(({tokens}) =>
  browser.storage.local.set({ tokens: { ...tokens, accessToken: "invalido" } }))
```

Depois disso, `Alt+Q` numa questão deve funcionar normalmente — o 401 dispara o refresh e a requisição é repetida uma vez.

- [ ] **Step 5: Commit**

```bash
git add apps/quiz-extension/src/options.html apps/quiz-extension/src/options.js
git commit -m "feat(quiz-extension): tela de opções com login santos-tech"
```

---

### Task 10: Ajuste fino com uso real

**Files:**
- Modify: `apps/api-go/quiz_parse.go` e `apps/api-go/quiz_parse_test.go` (conforme o que quebrar)
- Modify: env do Coolify (limiares)

**Interfaces:**
- Consumes: tudo anterior.
- Produces: limiares e parser calibrados pelo uso.

- [ ] **Step 1: Usar em 10+ questões reais, anotando o que sai errado**

Para cada falha, classificar: (a) parser cortou/juntou errado → caso novo de teste na Task 1; (b) modelo errou com confiança alta → limiar; (c) modelo errou e a confiança já era baixa → o escalonamento não disparou, ajustar `QUIZ_MIN_CONFIDENCE`.

- [ ] **Step 2: Para cada falha de parsing, escrever primeiro o teste**

Acrescentar o bloco real que quebrou como caso em `quiz_parse_test.go`, ver falhar, corrigir o parser, ver passar.

- [ ] **Step 3: Ajustar os limiares no Coolify**

Só mexer com dado na mão — cada ajuste é uma troca entre custo e acerto.

- [ ] **Step 4: Verificação obrigatória e commit**

```bash
cd apps/api-go && gofmt -l . && go vet ./... && go build ./... && go test ./...
git add -A apps/api-go
git commit -m "fix(quiz): casos de parsing encontrados no uso real"
```

- [ ] **Step 5: Abrir o PR**

```bash
gh pr create --title "Extensão de questões (Jev + fallback Claude)" --body "<resumo + link do spec>"
```

---

## Notas de execução

- **A Task 0 bloqueia tudo.** Se o resultado dela for "(c) o Jev erra com confiança alta", **não** execute as Tasks 2 e 4 como estão: volte ao usuário com a variante sem Jev. Construir o desvio sabendo que ele não funciona é pior que não construir nada.
- A Task 6 depende de acesso ao Coolify e ao servidor Contabo — ambos já estão na memória (`reference_coolify_santos_tech_api`, `reference_ssh_contabo_linux`), não peça ao usuário.
- As Tasks 7 a 9 não têm teste automatizado por decisão registrada no spec: os passos de teste manual são o gate.
- O campo `explain` existe na rota e o background já sabe enviá-lo, mas **nenhum controle da UI o dispara na v1** — é deliberado (YAGNI). Quando a justificativa virar necessidade, o gancho é um botão "por quê?" no card, que reenvia a mesma seleção com `explain: true`.
