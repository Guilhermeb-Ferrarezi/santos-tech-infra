package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Regras da agenda que o bot precisa obedecer em Go — não no prompt.
//
// O prompt já dizia "proponha um horário livre dentro do funcionamento". Isso
// funcionava porque um humano conferia antes de gravar. Sem esse humano, uma
// frase em português não é validação: é torcida. O que está aqui é o que
// impede, mecanicamente, aula marcada com a escola fechada ou em cima de outra.

// MarcadorExperimental — todo agendamento criado pelo bot começa assim.
//
// É o que separa o que é DELE do que é DE VOCÊS. Henrique e Rodrigo lançam
// aulas na mão na mesma base; o bot não pode tocar nessas. Sem um marcador no
// título não existe como distinguir depois, e "não mexer no que não é seu"
// viraria uma regra sem como ser verificada.
const MarcadorExperimental = "Aula experimental"

// TituloAgendamento monta o título da página no Notion.
func TituloAgendamento(aluno string) string {
	aluno = strings.TrimSpace(aluno)
	if aluno == "" {
		return MarcadorExperimental
	}
	return MarcadorExperimental + " — " + aluno
}

// EhAulaExperimental diz se o bot pode mexer neste agendamento.
//
// Conservador de propósito: na dúvida, responde false. O custo de um falso
// negativo é o bot não conseguir remarcar uma aula que era dele; o custo de um
// falso positivo é apagar um compromisso que alguém lançou na mão.
func EhAulaExperimental(titulo string) bool {
	t := strings.ToLower(strings.TrimSpace(titulo))
	return strings.HasPrefix(t, strings.ToLower(MarcadorExperimental))
}

// ── funcionamento ────────────────────────────────────────────────────────────

// JanelaFuncionamento — minutos desde a meia-noite em que a escola abre e fecha.
type JanelaFuncionamento struct {
	AbreMin  int
	FechaMin int
}

// ParseFuncionamento lê "08:00"/"22:00". Valor inválido devolve erro em vez de
// um default silencioso: horário de funcionamento errado é o tipo de coisa que
// só aparece quando a família chega e encontra a porta fechada.
func ParseFuncionamento(abre, fecha string) (JanelaFuncionamento, error) {
	a, err := minutosDoDia(abre)
	if err != nil {
		return JanelaFuncionamento{}, fmt.Errorf("ESCOLA_ABRE inválido (%q): %w", abre, err)
	}
	f, err := minutosDoDia(fecha)
	if err != nil {
		return JanelaFuncionamento{}, fmt.Errorf("ESCOLA_FECHA inválido (%q): %w", fecha, err)
	}
	if f <= a {
		return JanelaFuncionamento{}, fmt.Errorf("ESCOLA_FECHA (%s) não é depois de ESCOLA_ABRE (%s)", fecha, abre)
	}
	return JanelaFuncionamento{AbreMin: a, FechaMin: f}, nil
}

func minutosDoDia(hhmm string) (int, error) {
	p := strings.SplitN(strings.TrimSpace(hhmm), ":", 2)
	if len(p) != 2 {
		return 0, fmt.Errorf("formato esperado HH:MM")
	}
	h, err := strconv.Atoi(p[0])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("hora fora de 0–23")
	}
	m, err := strconv.Atoi(p[1])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("minuto fora de 0–59")
	}
	return h*60 + m, nil
}

// DentroDoFuncionamento: a aula inteira cabe no expediente?
//
// Repare que valida o FIM também. Uma aula de uma hora começando às 21h30 com
// a escola fechando às 22h deixa meia hora de aula com a porta trancada.
func (j JanelaFuncionamento) DentroDoFuncionamento(inicio time.Time, dur time.Duration) bool {
	ini := inicio.Hour()*60 + inicio.Minute()
	fim := ini + int(dur.Minutes())
	return ini >= j.AbreMin && fim <= j.FechaMin
}

// ── conflito ─────────────────────────────────────────────────────────────────

// Conflito procura, na grade da semana, alguma aula que ocupe o horário
// pedido. Devolve a que conflita, para o log e a mensagem dizerem qual.
//
// Compara DIA DA SEMANA e SOBREPOSIÇÃO de intervalo. Linha cujo horário não
// deu para interpretar não bloqueia: o código não finge que entendeu um texto
// que não entendeu — mas a linha continua visível no prompt, e o modelo vê.
func Conflito(inicio time.Time, dur time.Duration, agenda []ScheduleEntry) (ScheduleEntry, bool) {
	for _, e := range agenda {
		if !e.Ok {
			continue
		}
		if e.Intervalo.Ocupa(inicio, dur) {
			return e, true
		}
	}
	return ScheduleEntry{}, false
}

// ── validação completa ───────────────────────────────────────────────────────

// MotivoRecusa — por que um horário não pode ser marcado. String vazia = pode.
type MotivoRecusa string

const (
	RecusaPassado      MotivoRecusa = "o horário já passou"
	RecusaForaHorario  MotivoRecusa = "fora do horário de funcionamento"
	RecusaConflito     MotivoRecusa = "já existe aula nesse horário"
	RecusaSemAntecedes MotivoRecusa = "antecedência insuficiente"
)

// AntecedenciaMinima — ninguém marca aula experimental para daqui a dez minutos.
// A escola precisa preparar sala e avisar o professor.
const AntecedenciaMinima = 2 * time.Hour

// PodeMarcar reúne todas as travas num lugar só. Devolve "" quando está livre.
func PodeMarcar(inicio time.Time, dur time.Duration, agora time.Time, j JanelaFuncionamento, agenda []ScheduleEntry) MotivoRecusa {
	if !inicio.After(agora) {
		return RecusaPassado
	}
	if inicio.Sub(agora) < AntecedenciaMinima {
		return RecusaSemAntecedes
	}
	if !j.DentroDoFuncionamento(inicio, dur) {
		return RecusaForaHorario
	}
	if _, bateu := Conflito(inicio, dur, agenda); bateu {
		return RecusaConflito
	}
	return ""
}
