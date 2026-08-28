package main

import (
	"fmt"
	"strconv"
	"time"
)

const agendaCapacidadeMaximaPCs = 10

var agendaTiposArena = map[string]bool{"avulso": true, "corujao": true, "mix": true}
var agendaTiposAula = map[string]bool{"aula_turma": true, "aula_particular": true, "aula_experimental": true}

// parseHoraMinutos aceita "HH:MM" ou "HH:MM:SS" (Postgres TIME::text inclui
// segundos) e devolve minutos desde 00:00.
func parseHoraMinutos(s string) (int, error) {
	if len(s) < 5 {
		return 0, fmt.Errorf("hora inválida: %q", s)
	}
	h, errH := strconv.Atoi(s[0:2])
	m, errM := strconv.Atoi(s[3:5])
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("hora inválida: %q", s)
	}
	return h*60 + m, nil
}

func parseData(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}

// AgendaOcorrencia é uma instância concreta de um evento (já resolvida a
// partir da recorrência) numa data específica.
type AgendaOcorrencia struct {
	EventoID           string
	Tipo               string
	Data               time.Time
	HoraInicioMin      int
	HoraFimMin         int
	ComputadoresUsados int
}

// resolveOcorrencias devolve as ocorrências concretas de um evento dentro de
// [rangeStart, rangeEnd] (inclusive). Não-recorrente gera no máximo 1
// ocorrência; "semanal" gera uma por semana no dia configurado, entre
// data_inicio e data_fim_recorrencia (ambos limitados ao range pedido).
func resolveOcorrencias(ev AgendaEvento, rangeStart, rangeEnd time.Time) ([]AgendaOcorrencia, error) {
	horaInicioMin, err := parseHoraMinutos(ev.HoraInicio)
	if err != nil {
		return nil, err
	}
	horaFimMin, err := parseHoraMinutos(ev.HoraFim)
	if err != nil {
		return nil, err
	}
	dataInicio, err := parseData(ev.DataInicio)
	if err != nil {
		return nil, fmt.Errorf("dataInicio inválida: %w", err)
	}

	mk := func(d time.Time) AgendaOcorrencia {
		return AgendaOcorrencia{
			EventoID: ev.ID, Tipo: ev.Tipo, Data: d,
			HoraInicioMin: horaInicioMin, HoraFimMin: horaFimMin,
			ComputadoresUsados: ev.ComputadoresUsados,
		}
	}

	if ev.Recorrencia != "semanal" {
		if dataInicio.Before(rangeStart) || dataInicio.After(rangeEnd) {
			return nil, nil
		}
		return []AgendaOcorrencia{mk(dataInicio)}, nil
	}

	if ev.DiaSemana == nil || ev.DataFimRecorrencia == nil {
		return nil, fmt.Errorf("evento semanal sem diaSemana/dataFimRecorrencia")
	}
	fimRecorrencia, err := parseData(*ev.DataFimRecorrencia)
	if err != nil {
		return nil, fmt.Errorf("dataFimRecorrencia inválida: %w", err)
	}

	start := dataInicio
	if rangeStart.After(start) {
		start = rangeStart
	}
	end := fimRecorrencia
	if rangeEnd.Before(end) {
		end = rangeEnd
	}
	if start.After(end) {
		return nil, nil
	}
	for int(start.Weekday()) != *ev.DiaSemana {
		start = start.AddDate(0, 0, 1)
		if start.After(end) {
			return nil, nil
		}
	}
	var out []AgendaOcorrencia
	for d := start; !d.After(end); d = d.AddDate(0, 0, 7) {
		out = append(out, mk(d))
	}
	return out, nil
}

func overlapsMin(aInicio, aFim, bInicio, bFim int) bool {
	return aInicio < bFim && bInicio < aFim
}

// AgendaConflitos é o resultado de avaliar um evento candidato contra os
// eventos existentes.
type AgendaConflitos struct {
	CapacidadeExcedida bool
	PCsOcupados        int
	EventosCapacidade  []AgendaEvento
	EventosPolitica    []AgendaEvento
}

// checkConflitos resolve TODAS as ocorrências do candidato no seu próprio
// intervalo de recorrência e, para cada uma, soma PCs de tudo que se
// sobrepõe naquele dia. Simplificação deliberada: EventosCapacidade/PCsOcupados
// refletem o PIOR dia entre as ocorrências do candidato, não um resultado
// por-dia — suficiente pro aviso na criação, que é o caso de uso real.
func checkConflitos(candidato AgendaEvento, existentes []AgendaEvento) (AgendaConflitos, error) {
	// Slices inicializados vazios (não nil): o front trata os dois campos como
	// array sempre presente (AgendaEvento[]) e o encoding/json serializa slice
	// nil como `null`, não `[]` — sem isso, o caso comum (nenhum conflito de
	// política) manda `eventosPolitica: null` e quebra `dryRun.eventosPolitica.length`.
	resultado := AgendaConflitos{
		EventosCapacidade: []AgendaEvento{},
		EventosPolitica:   []AgendaEvento{},
	}

	// dia_inteiro é um banner (ex.: "Recesso escolar") — não ocupa PC nem
	// disputa horário com nada, então nem entra no motor de conflito.
	if candidato.Tipo == "dia_inteiro" {
		return resultado, nil
	}

	rangeStart, err := parseData(candidato.DataInicio)
	if err != nil {
		return resultado, err
	}
	rangeEnd := rangeStart
	if candidato.Recorrencia == "semanal" && candidato.DataFimRecorrencia != nil {
		rangeEnd, err = parseData(*candidato.DataFimRecorrencia)
		if err != nil {
			return resultado, err
		}
	}

	candidatoOcorrencias, err := resolveOcorrencias(candidato, rangeStart, rangeEnd)
	if err != nil {
		return resultado, err
	}

	capacidadeIDs := map[string]bool{}
	politicaIDs := map[string]bool{}
	maxPCsOcupados := 0

	for _, co := range candidatoOcorrencias {
		pcsNoDia := co.ComputadoresUsados
		for _, ev := range existentes {
			if ev.ID == candidato.ID {
				continue // update: não compara consigo mesmo
			}
			if ev.Tipo == "dia_inteiro" {
				continue // banner de dia inteiro nunca ocupa PC nem conta pra política
			}
			ocorrenciasEv, err := resolveOcorrencias(ev, co.Data, co.Data)
			if err != nil {
				return resultado, err
			}
			for _, oev := range ocorrenciasEv {
				if !overlapsMin(co.HoraInicioMin, co.HoraFimMin, oev.HoraInicioMin, oev.HoraFimMin) {
					continue
				}
				pcsNoDia += oev.ComputadoresUsados
				capacidadeIDs[ev.ID] = true
				arenaAula := agendaTiposArena[candidato.Tipo] && agendaTiposAula[ev.Tipo]
				aulaArena := agendaTiposAula[candidato.Tipo] && agendaTiposArena[ev.Tipo]
				if arenaAula || aulaArena {
					politicaIDs[ev.ID] = true
				}
			}
		}
		if pcsNoDia > maxPCsOcupados {
			maxPCsOcupados = pcsNoDia
		}
	}

	resultado.PCsOcupados = maxPCsOcupados
	resultado.CapacidadeExcedida = maxPCsOcupados > agendaCapacidadeMaximaPCs
	for _, ev := range existentes {
		if resultado.CapacidadeExcedida && capacidadeIDs[ev.ID] {
			resultado.EventosCapacidade = append(resultado.EventosCapacidade, ev)
		}
		if politicaIDs[ev.ID] {
			resultado.EventosPolitica = append(resultado.EventosPolitica, ev)
		}
	}
	return resultado, nil
}
