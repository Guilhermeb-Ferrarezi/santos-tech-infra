package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

var errAgendaEventoNotFound = appErr(http.StatusNotFound, "AGENDA_EVENTO_NOT_FOUND", "Evento não encontrado")
var errAgendaFeriadoNotFound = appErr(http.StatusNotFound, "AGENDA_FERIADO_NOT_FOUND", "Feriado não encontrado")

func agendaEventoIDFrom(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		return "", errAgendaEventoNotFound
	}
	return id, nil
}

func validateAgendaEventoInput(in *AgendaEventoInput) error {
	in.Titulo = strings.TrimSpace(in.Titulo)
	if in.Titulo == "" {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Título obrigatório")
	}
	if !validAgendaTipos[in.Tipo] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Tipo inválido")
	}
	if in.ComputadoresUsados < 0 {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Quantidade de computadores inválida")
	}
	if _, err := time.Parse("2006-01-02", in.DataInicio); err != nil {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Data de início inválida")
	}
	hi, err := parseHoraMinutos(in.HoraInicio)
	if err != nil {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Hora de início inválida")
	}
	hf, err := parseHoraMinutos(in.HoraFim)
	if err != nil {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Hora de fim inválida")
	}
	if hf <= hi {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Hora de fim deve ser depois da hora de início")
	}

	// Só aula_turma é recorrente — regra fixa da spec, não é escolha do usuário.
	if in.Tipo == "aula_turma" {
		in.Recorrencia = "semanal"
	} else {
		in.Recorrencia = "nenhuma"
	}
	if in.Recorrencia == "semanal" {
		if in.DiaSemana == nil || *in.DiaSemana < 0 || *in.DiaSemana > 6 {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "Dia da semana obrigatório para evento recorrente")
		}
		if in.DataFimRecorrencia == nil || strings.TrimSpace(*in.DataFimRecorrencia) == "" {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "Data de fim da recorrência obrigatória")
		}
		fim, err := time.Parse("2006-01-02", *in.DataFimRecorrencia)
		if err != nil {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "Data de fim da recorrência inválida")
		}
		inicio, _ := time.Parse("2006-01-02", in.DataInicio)
		if !fim.After(inicio) {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "Data de fim da recorrência deve ser depois do início")
		}
	} else {
		in.DiaSemana = nil
		in.DataFimRecorrencia = nil
	}

	if agendaTiposArena[in.Tipo] {
		if in.StatusPreparo == nil || *in.StatusPreparo == "" {
			pendente := "pendente"
			in.StatusPreparo = &pendente
		} else if !validAgendaStatusPreparo[*in.StatusPreparo] {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "Status de preparo inválido")
		}
	} else {
		naoAplica := "nao_aplica"
		in.StatusPreparo = &naoAplica
	}
	return nil
}

// GET /agenda/eventos
func (s *Server) handleListAgendaEventos(w http.ResponseWriter, r *http.Request) {
	eventos, err := s.listAgendaEventos(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"eventos": eventos})
}

// GET /agenda/eventos/{id}
func (s *Server) handleGetAgendaEvento(w http.ResponseWriter, r *http.Request) {
	id, err := agendaEventoIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	evento, err := s.getAgendaEvento(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if evento == nil {
		writeErr(w, errAgendaEventoNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evento": evento})
}

// avaliaConflitos roda a validação de input + o motor de capacidade/política
// contra os eventos já existentes. Devolve (conflitos, podeGravar) — usado
// tanto por create/update (que gravam se podeGravar) quanto pelo dry-run de
// POST /agenda/eventos/check (que nunca grava).
func (s *Server) avaliaConflitos(w http.ResponseWriter, r *http.Request, in *AgendaEventoInput, candidatoID string) (AgendaConflitos, bool) {
	if err := validateAgendaEventoInput(in); err != nil {
		writeErr(w, err)
		return AgendaConflitos{}, false
	}
	existentes, err := s.listAgendaEventos(r.Context())
	if err != nil {
		writeErr(w, err)
		return AgendaConflitos{}, false
	}
	candidato := in.toEvento(candidatoID)
	conflitos, err := checkConflitos(candidato, existentes)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", err.Error()))
		return AgendaConflitos{}, false
	}
	if conflitos.CapacidadeExcedida {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code":                "AGENDA_CAPACIDADE_EXCEDIDA",
			"message":             "Capacidade de PCs excedida nesse horário",
			"pcsOcupados":         conflitos.PCsOcupados,
			"eventosConflitantes": conflitos.EventosCapacidade,
		})
		return conflitos, false
	}
	if len(conflitos.EventosPolitica) > 0 && !in.ConfirmarConflito {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":                "AGENDA_CONFLITO_POLITICA",
			"message":             "Esse horário conflita com uma aula. Confirme se deseja mesmo assim.",
			"eventosConflitantes": conflitos.EventosPolitica,
		})
		return conflitos, false
	}
	return conflitos, true
}

// POST /agenda/eventos/check — dry-run: roda o mesmo motor de conflito sem
// gravar nada. Usado pelo formulário do front pra mostrar capacidade em
// tempo real enquanto o usuário preenche.
func (s *Server) handleCheckAgendaEvento(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in AgendaEventoInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateAgendaEventoInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	existentes, err := s.listAgendaEventos(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	conflitos, err := checkConflitos(in.toEvento(""), existentes)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capacidadeExcedida": conflitos.CapacidadeExcedida,
		"pcsOcupados":        conflitos.PCsOcupados,
		"eventosCapacidade":  conflitos.EventosCapacidade,
		"eventosPolitica":    conflitos.EventosPolitica,
	})
}

// POST /agenda/eventos
func (s *Server) handleCreateAgendaEvento(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in AgendaEventoInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	conflitos, ok := s.avaliaConflitos(w, r, &in, "")
	if !ok {
		return
	}
	evento, err := s.insertAgendaEvento(r.Context(), in, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(conflitos.EventosPolitica) > 0 && in.ConfirmarConflito {
		ids := make([]string, len(conflitos.EventosPolitica))
		for i, e := range conflitos.EventosPolitica {
			ids[i] = e.ID
		}
		if err := s.insertAgendaEventoConfirmacao(r.Context(), evento.ID, userIDFrom(r), ids); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"evento": evento})
}

// PUT /agenda/eventos/{id}
func (s *Server) handleUpdateAgendaEvento(w http.ResponseWriter, r *http.Request) {
	id, err := agendaEventoIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in AgendaEventoInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	current, err := s.getAgendaEvento(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if current == nil {
		writeErr(w, errAgendaEventoNotFound)
		return
	}
	conflitos, ok := s.avaliaConflitos(w, r, &in, id)
	if !ok {
		return
	}
	evento, err := s.updateAgendaEvento(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(conflitos.EventosPolitica) > 0 && in.ConfirmarConflito {
		ids := make([]string, len(conflitos.EventosPolitica))
		for i, e := range conflitos.EventosPolitica {
			ids[i] = e.ID
		}
		if err := s.insertAgendaEventoConfirmacao(r.Context(), evento.ID, userIDFrom(r), ids); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"evento": evento})
}

// DELETE /agenda/eventos/{id}
func (s *Server) handleDeleteAgendaEvento(w http.ResponseWriter, r *http.Request) {
	id, err := agendaEventoIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.deleteAgendaEvento(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /agenda/feriados?ano=2026
func (s *Server) handleListAgendaFeriados(w http.ResponseWriter, r *http.Request) {
	ano, err := strconv.Atoi(r.URL.Query().Get("ano"))
	if err != nil || ano < 2000 || ano > 2100 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Parâmetro ano inválido"))
		return
	}
	feriados, err := s.listAgendaFeriados(r.Context(), ano)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feriados": feriados})
}

// ── Feriados municipais — admin-only ────────────────────────────────────────

// GET /agenda/feriados-municipais
func (s *Server) handleListAgendaFeriadosMunicipais(w http.ResponseWriter, r *http.Request) {
	feriados, err := s.listAgendaFeriadosMunicipais(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feriados": feriados})
}

// POST /agenda/feriados-municipais
func (s *Server) handleCreateAgendaFeriadoMunicipal(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Data string `json:"data"`
		Nome string `json:"nome"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Nome = strings.TrimSpace(in.Nome)
	if in.Nome == "" {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Nome obrigatório"))
		return
	}
	if _, err := time.Parse("2006-01-02", in.Data); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Data inválida"))
		return
	}
	feriado, err := s.insertAgendaFeriadoMunicipal(r.Context(), in.Data, in.Nome)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"feriado": feriado})
}

// DELETE /agenda/feriados-municipais/{id}
func (s *Server) handleDeleteAgendaFeriadoMunicipal(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, errAgendaFeriadoNotFound)
		return
	}
	if err := s.deleteAgendaFeriadoMunicipal(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
