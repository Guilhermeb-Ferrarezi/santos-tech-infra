package main

import (
	"regexp"
	"strings"
)

// Tradução da base "Agenda de Aulas" do Notion para turmas + horários do
// Portal. Fica separado do acesso a rede e a banco de propósito: é aqui que
// mora a interpretação de um dado bagunçado (nome de aluno em campo de texto,
// horário em 3 formatos, uma opção com 4 alunos dentro), então precisa ser
// testável linha a linha.

type notionPlanAction string

const (
	notionPlanCriar    notionPlanAction = "criar"
	notionPlanIgnorado notionPlanAction = "ignorado"
)

// notionPlanItem é uma linha do Notion já traduzida: qual turma ela vira e
// qual horário semanal ela representa.
type notionPlanItem struct {
	NotionPageID string           `json:"notionPageId"`
	TurmaKey     string           `json:"turmaKey"` // identidade estável da turma
	Turma        string           `json:"turma"`
	Curso        string           `json:"curso"`
	Alunos       []string         `json:"alunos"`
	DayOfWeek    int16            `json:"dayOfWeek"`
	StartTime    string           `json:"startTime"`
	EndTime      string           `json:"endTime"`
	Professor    string           `json:"professor"`
	Action       notionPlanAction `json:"action"`
	Motivo       string           `json:"motivo,omitempty"`
}

var notionDias = map[string]int16{
	"domingo": 0, "segunda": 1, "terça": 2, "terca": 2, "quarta": 3,
	"quinta": 4, "sexta": 5, "sábado": 6, "sabado": 6,
}

// notionHoraRe casa os 3 formatos que existem na base: "18:00–19:00" (travessão),
// "16:00 ~ 18:00" (til) e "16h00-17h00" (h). Pegar os pares de dígitos cobre os
// três sem precisar de um regex por formato.
var notionHoraRe = regexp.MustCompile(`(\d{1,2})[:hH](\d{2})`)

func notionParseHorario(s string) (string, string, bool) {
	m := notionHoraRe.FindAllStringSubmatch(s, -1)
	if len(m) != 2 {
		return "", "", false
	}
	norm := func(h, min string) string {
		if len(h) == 1 {
			h = "0" + h
		}
		return h + ":" + min
	}
	ini, fim := norm(m[0][1], m[0][2]), norm(m[1][1], m[1][2])
	if !portalTimeRe.MatchString(ini) || !portalTimeRe.MatchString(fim) || ini >= fim {
		return "", "", false
	}
	return ini, fim, true
}

// notionLimpaNome tira o ruído que os nomes carregam no Notion: o assunto
// colado depois de " - " ("Walisson - Informática"), e as observações entre
// parênteses ("Igor (pai: Hamilton)", "... (TER e QUI) ...").
func notionLimpaNome(s string) string {
	s = regexp.MustCompile(`\s*\([^)]*\)`).ReplaceAllString(s, " ")
	if i := strings.Index(s, " - "); i >= 0 {
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}

// notionSeparaAlunos quebra uma opção que junta várias pessoas
// ("Paloma Felipe José e João") numa lista. Heurística deliberadamente
// conservadora: só separa no " e " e em vírgula. Um nome composto de uma
// pessoa só continua inteiro.
func notionSeparaAlunos(tag string) []string {
	limpo := notionLimpaNome(tag)
	if limpo == "" {
		return nil
	}
	partes := regexp.MustCompile(`\s*(?:,|\s+e\s+)\s*`).Split(limpo, -1)
	out := make([]string, 0, len(partes))
	for _, p := range partes {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// planNotionAgenda traduz as linhas do Notion em itens de plano. Não toca em
// banco: o que existe ou não no Portal é decidido depois, na aplicação.
func planNotionAgenda(rows []notionAulaRow) []notionPlanItem {
	out := make([]notionPlanItem, 0, len(rows))
	for _, r := range rows {
		item := notionPlanItem{NotionPageID: r.PageID, Professor: r.Professor}

		titulo := strings.TrimSpace(r.Aula)
		tag := ""
		if len(r.Aluno) > 0 {
			tag = strings.TrimSpace(r.Aluno[0])
		}

		// A turma é identificada pela TAG quando existe (é o que agrupa as
		// aulas da mesma pessoa/turma em dias diferentes) e pelo título quando
		// não existe. Sem isso, "Turma Informática" viraria uma turma só,
		// misturando dois grupos de alunos diferentes.
		if tag != "" {
			item.TurmaKey = "aluno:" + strings.ToLower(tag)
			nome := notionLimpaNome(tag)
			if strings.HasPrefix(strings.ToLower(titulo), "turma") {
				item.Turma = titulo + " · " + nome
			} else {
				item.Turma = nome
			}
			item.Alunos = notionSeparaAlunos(tag)
		} else {
			item.TurmaKey = "aula:" + strings.ToLower(titulo)
			item.Turma = titulo
			item.Alunos = []string{titulo}
		}
		item.Curso = strings.TrimSpace(r.Conteudo)

		switch {
		case titulo == "" || (r.Dia == "" && r.Horario == ""):
			item.Action, item.Motivo = notionPlanIgnorado, "linha sem dia nem horário (cabeçalho/rascunho)"
		case strings.Contains(strings.ToLower(titulo), "experimental"):
			item.Action, item.Motivo = notionPlanIgnorado, "aula experimental: evento pontual, não é turma recorrente"
		case item.Curso == "":
			item.Action, item.Motivo = notionPlanIgnorado, "sem Conteúdo no Notion — não dá pra dizer qual curso é"
		default:
			dia, okDia := notionDias[strings.ToLower(strings.TrimSpace(r.Dia))]
			ini, fim, okHora := notionParseHorario(r.Horario)
			switch {
			case !okDia:
				item.Action, item.Motivo = notionPlanIgnorado, "dia da semana ausente ou não reconhecido: "+r.Dia
			case !okHora:
				item.Action, item.Motivo = notionPlanIgnorado, "horário não interpretável: "+r.Horario
			default:
				item.DayOfWeek, item.StartTime, item.EndTime = dia, ini, fim
				item.Action = notionPlanCriar
			}
		}
		out = append(out, item)
	}
	return out
}
