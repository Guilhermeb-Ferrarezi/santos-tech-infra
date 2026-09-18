package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Gerador de currículo — a parte PURA da reescrita assistida por IA: montar o
// brief e interpretar o que o Claude devolve. Nada aqui toca banco, rede ou
// relógio (mesmo desenho de posaula_prompt.go). Quem orquestra é
// curriculo_gerar.go.
//
// O botão "Melhorar com IA" pega um campo já escrito pela pessoa (resumo,
// objetivo, um tópico de experiência ou a descrição de um projeto) e reescreve
// na fórmula do guia do kit — verbo de ação + atividade + método/escala +
// resultado — usando SÓ os fatos que já estão no formulário. Nunca inventa
// número, ferramenta ou empresa que a pessoa não escreveu.

// curriculoPromptVersion não entra em nenhuma chave de idempotência hoje (não
// há reentrega a considerar — cada pedido de reescrita é uma linha nova), mas
// documenta a versão do prompt pra quando algum dia precisar comparar saídas
// antigas com o prompt atual.
const curriculoPromptVersion = "v1"

const (
	// curriculoTextoOriginalMax: teto do rascunho que a pessoa manda reescrever
	// — mais que isso não é "um tópico de currículo", é colagem de outra coisa.
	curriculoTextoOriginalMax = 2_000
	// curriculoMaxCharsMinimo/Maximo: o maxChars que o front manda (limite do
	// campo no Zod) é usado como instrução ao modelo, mas com teto próprio —
	// nunca confiar cegamente num número vindo do cliente.
	curriculoMaxCharsMinimo = 40
	curriculoMaxCharsMaximo = 600
)

// curriculoCampos: os únicos campos que este botão sabe reescrever — cada um
// tem um nome em português usado no prompt (a pessoa nunca vê a chave).
var curriculoCampos = map[string]string{
	"resumo":   "resumo profissional",
	"objetivo": "objetivo profissional",
	"topico":   "tópico de experiência (uma linha do currículo)",
	"projeto":  "descrição de projeto",
}

func curriculoCampoValido(campo string) bool {
	_, ok := curriculoCampos[campo]
	return ok
}

// curriculoCampoPermiteGeracao: só "objetivo" pode ser pedido com texto
// vazio — os outros campos (resumo, tópico, projeto) são sempre uma
// REESCRITA de um rascunho que a pessoa já tem; "objetivo" também serve pra
// GERAR do zero, pensando em quem nunca trabalhou e não sabe por onde
// começar a escrever um objetivo profissional.
func curriculoCampoPermiteGeracao(campo string) bool {
	return campo == "objetivo"
}

// curriculoContexto é o que o formulário já tem preenchido — vira parte do
// brief pra a reescrita (ou geração) não inventar fatos que a pessoa não
// informou. Tudo opcional: a pessoa pode estar reescrevendo o resumo antes
// de preencher o resto.
type curriculoContexto struct {
	Cargo              string   `json:"cargo,omitempty"`
	TituloProfissional string   `json:"tituloProfissional,omitempty"`
	VagaCargo          string   `json:"vagaCargo,omitempty"`
	VagaEmpresa        string   `json:"vagaEmpresa,omitempty"`
	ExperienciaCargo   string   `json:"experienciaCargo,omitempty"`
	ExperienciaEmpresa string   `json:"experienciaEmpresa,omitempty"`
	Competencias       []string `json:"competencias,omitempty"`
	// Formacao: um resumo por item ("Ensino Médio — Escola Tal, cursando").
	// Importa principalmente pra GERAR o objetivo de quem nunca trabalhou —
	// sem experiência, a formação é o principal fato disponível.
	Formacao []string `json:"formacao,omitempty"`
}

// briefCurriculoInput é tudo que o prompt precisa pra reescrever um campo.
type briefCurriculoInput struct {
	Campo         string // chave de curriculoCampos
	MaxChars      int
	TextoOriginal string
	Contexto      curriculoContexto
	// ErroAnterior: na 2ª tentativa, o motivo do parse ter falhado na 1ª.
	ErroAnterior string
}

// montarBriefCurriculoReescrita monta o prompt em português: identidade →
// contexto já preenchido → o rascunho a reescrever (ou aviso de que não há
// rascunho, pra GERAR do zero) → instruções de saída.
func montarBriefCurriculoReescrita(in briefCurriculoInput) string {
	var b strings.Builder
	b.WriteString("Você ajuda pessoas a escrever currículos profissionais em português do Brasil, no estilo direto e objetivo pedido por sistemas de triagem automática (ATS): frases curtas, verbo de ação no início, sem adjetivos vazios (\"excelente\", \"apaixonado\").\n\n")

	campoNome := curriculoCampos[in.Campo]
	temTextoOriginal := strings.TrimSpace(in.TextoOriginal) != ""
	if temTextoOriginal {
		fmt.Fprintf(&b, "Sua tarefa: reescrever o %s abaixo, melhorando a clareza e a força, SEM inventar nenhum fato, número, ferramenta ou empresa que não esteja no texto original ou no contexto fornecido.\n\n", campoNome)
	} else {
		fmt.Fprintf(&b, "Sua tarefa: ESCREVER um %s do zero, coerente com o contexto abaixo — a pessoa ainda não escreveu nada. Use só os fatos do contexto (vaga-alvo, formação, competências); muita gente que usa isto está buscando o PRIMEIRO emprego e não tem experiência nenhuma — nesse caso, apoie o texto na formação/competências, sem jamais inventar uma experiência que a pessoa não teve.\n\n", campoNome)
	}

	if in.Contexto.VagaCargo != "" || in.Contexto.VagaEmpresa != "" {
		fmt.Fprintf(&b, "## Vaga-alvo\nCargo: %s\nEmpresa: %s\n\n", vazioOu(in.Contexto.VagaCargo, "(não informado)"), vazioOu(in.Contexto.VagaEmpresa, "(não informada)"))
	}
	if in.Contexto.TituloProfissional != "" {
		fmt.Fprintf(&b, "## Título profissional da pessoa\n%s\n\n", in.Contexto.TituloProfissional)
	}
	if in.Contexto.ExperienciaCargo != "" || in.Contexto.ExperienciaEmpresa != "" {
		fmt.Fprintf(&b, "## Esta experiência é\nCargo: %s\nEmpresa: %s\n\n", vazioOu(in.Contexto.ExperienciaCargo, "(não informado)"), vazioOu(in.Contexto.ExperienciaEmpresa, "(não informada)"))
	}
	if len(in.Contexto.Competencias) > 0 {
		fmt.Fprintf(&b, "## Competências que a pessoa já listou\n%s\n\n", strings.Join(in.Contexto.Competencias, ", "))
	}
	if len(in.Contexto.Formacao) > 0 {
		fmt.Fprintf(&b, "## Formação da pessoa\n%s\n\n", strings.Join(in.Contexto.Formacao, "; "))
	}

	if temTextoOriginal {
		fmt.Fprintf(&b, "## Texto original (rascunho da pessoa)\n%s\n\n", strings.TrimSpace(in.TextoOriginal))
	}

	if in.Campo == "topico" || in.Campo == "projeto" {
		b.WriteString("Formato esperado para este campo: verbo de ação + o que foi feito + método/ferramenta/escala (se souber) + resultado (se souber). Se o texto original não menciona método ou resultado, NÃO invente — reescreva só o que já está lá, de forma mais clara.\n\n")
	}

	b.WriteString("## O que você deve produzir\n")
	b.WriteString("Responda SOMENTE com um JSON válido, sem texto antes ou depois e sem crases de markdown, neste formato exato:\n")
	b.WriteString(`{"texto": "..."}` + "\n\n")
	fmt.Fprintf(&b, "Regras:\n- texto: até %d caracteres — se não couber, corte o menos importante, nunca invente pra preencher espaço.\n", in.MaxChars)
	b.WriteString("- Português do Brasil, sem emoji, sem markdown dentro do texto.\n")
	if temTextoOriginal {
		b.WriteString("- Se o texto original já estiver bom, pode devolver quase igual — o objetivo é melhorar, não mudar por mudar.\n")
	}
	if strings.TrimSpace(in.ErroAnterior) != "" {
		fmt.Fprintf(&b, "\nATENÇÃO: sua resposta anterior foi rejeitada por este motivo: %s. Corrija e devolva só o JSON.\n", strings.TrimSpace(in.ErroAnterior))
	}
	return b.String()
}

type curriculoRewriteOut struct {
	Texto string `json:"texto"`
}

// parseCurriculoRewrite interpreta e valida a resposta do modelo. maxChars
// limita o resultado (truncado por caractere, nunca por byte); vazio é erro
// — uma reescrita vazia não serve pra nada e o front trataria como "sem
// mudança" de forma confusa.
func parseCurriculoRewrite(text string, maxChars int) (curriculoRewriteOut, error) {
	raw, ok := extrairJSON(text)
	if !ok {
		return curriculoRewriteOut{}, errors.New("não encontrei um objeto JSON na resposta")
	}
	var out curriculoRewriteOut
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return curriculoRewriteOut{}, fmt.Errorf("JSON inválido: %v", err)
	}
	out.Texto = truncarRunas(strings.TrimSpace(out.Texto), maxChars)
	if out.Texto == "" {
		return curriculoRewriteOut{}, errors.New("texto vazio")
	}
	return out, nil
}

// curriculoMaxCharsClamp aplica o teto de segurança ao maxChars vindo do
// cliente (o Zod do front já limita por campo, mas o backend nunca confia
// cegamente num número vindo de fora).
func curriculoMaxCharsClamp(v int) int {
	if v < curriculoMaxCharsMinimo {
		return curriculoMaxCharsMinimo
	}
	if v > curriculoMaxCharsMaximo {
		return curriculoMaxCharsMaximo
	}
	return v
}
