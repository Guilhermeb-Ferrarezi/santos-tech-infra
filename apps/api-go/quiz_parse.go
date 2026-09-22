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
