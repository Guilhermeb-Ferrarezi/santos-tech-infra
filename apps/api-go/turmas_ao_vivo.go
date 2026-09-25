package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santos-tech/auth/db"
)

// Turmas ao vivo — o que o bot de vendas consulta pra falar de turma com
// dado real. Ver dashboard docs/superpowers/specs/2026-09-25-bot-turmas-ao-vivo-design.md.
//
// Divisão de fontes: a turma do Portal (class) diz QUEM é a turma (curso,
// início, fim, matrículas, capacidade); a Agenda diz QUANDO ela ocupa o
// laboratório. As duas se ligam por agenda_eventos.portal_class_id.

var errTurmaPortalNotFound = appErr(http.StatusNotFound, "TURMA_NOT_FOUND", "Turma não encontrada")

// validaCapacidadeTurma: nil = não informado (vale o padrão/valor atual). O
// teto de 50 espelha o CHECK da coluna — sem ele um número absurdo viraria 500
// do banco em vez de 400 legível.
func validaCapacidadeTurma(c *int) error {
	if c != nil && (*c < 1 || *c > 50) {
		return validationErr("capacidade deve ficar entre 1 e 50 alunos")
	}
	return nil
}

// agendaTipoCombinaComTurma: aula_turma só se liga a turma de grupo e
// aula_particular só a turma particular. Os demais tipos (experimental,
// Arena, dia inteiro) não pertencem a turma nenhuma. Sem essa trava, uma
// particular ligada a turma de grupo apareceria pro bot como horário da turma.
func agendaTipoCombinaComTurma(tipo string, individual bool) bool {
	switch tipo {
	case "aula_turma":
		return !individual
	case "aula_particular":
		return individual
	default:
		return false
	}
}

// portalClassIndividual devolve individual_class da turma; (false, false) se
// ela não existe.
func (s *Server) portalClassIndividual(ctx context.Context, classID int64) (individual, existe bool, err error) {
	err = s.portalDB.QueryRow(ctx, `SELECT individual_class FROM class WHERE id = $1`, classID).Scan(&individual)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return individual, true, nil
}

// PUT /agenda/eventos/{id}/turma {portalClassId: int|null}
//
// Rota própria de propósito: o PUT do evento substitui o evento inteiro, e
// quem monta o corpo sem este campo (popover rápido da Agenda, tool do MCP)
// apagaria a ligação sem ninguém ver.
func (s *Server) handleSetAgendaEventoTurma(w http.ResponseWriter, r *http.Request) {
	id, err := agendaEventoIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		PortalClassID *int64 `json:"portalClassId"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	ev, err := s.getAgendaEvento(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev == nil {
		writeErr(w, errAgendaEventoNotFound)
		return
	}
	var classID pgtype.Int4
	if in.PortalClassID != nil {
		if *in.PortalClassID <= 0 || *in.PortalClassID > 1<<31-1 {
			writeErr(w, errTurmaPortalNotFound)
			return
		}
		individual, existe, err := s.portalClassIndividual(r.Context(), *in.PortalClassID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !existe {
			writeErr(w, errTurmaPortalNotFound)
			return
		}
		if !agendaTipoCombinaComTurma(ev.Tipo, individual) {
			writeErr(w, appErr(http.StatusBadRequest, "AGENDA_TURMA_TIPO_INCOMPATIVEL",
				"Aula de turma só se liga a turma de grupo, e aula particular só a turma particular"))
			return
		}
		classID = pgtype.Int4{Int32: int32(*in.PortalClassID), Valid: true}
	}
	if _, err := s.q.SetAgendaEventoPortalClass(r.Context(), db.SetAgendaEventoPortalClassParams{
		PortalClassID: classID, ID: uuidToPg(id),
	}); err != nil {
		writeErr(w, err)
		return
	}
	ev, err = s.getAgendaEvento(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evento": ev})
}

// motivoTurmaNaoLiberavel diz por que a turma não pode aparecer pro bot, ou
// "" se pode. Cada motivo vira texto de tela — é o que a pessoa corrige.
func motivoTurmaNaoLiberavel(c *portalClassDTO, horariosLigados int, hoje time.Time) string {
	switch {
	case c.IndividualClass:
		return "Aula particular não aparece para o bot — só turma de grupo"
	case c.EndDate.Before(truncaDia(hoje)):
		return "O fim da turma já passou — corrija o fim antes de liberar"
	case horariosLigados == 0:
		return "Ligue pelo menos um horário da Agenda (aula de turma) a esta turma antes de liberar"
	}
	return ""
}

func truncaDia(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// horariosDaTurma: eventos aula_turma da Agenda ligados à turma. Ligação
// incoerente (outro tipo apontando pra turma — ex.: evento que mudou de tipo
// depois de ligado) é ignorada: fail-closed.
func horariosDaTurma(eventos []AgendaEvento, classID int64) []AgendaEvento {
	var out []AgendaEvento
	for _, e := range eventos {
		if e.PortalClassID != nil && *e.PortalClassID == classID && e.Tipo == "aula_turma" && e.Recorrencia == "semanal" {
			out = append(out, e)
		}
	}
	return out
}

// POST /portal/classes/{classId}/bot-validacao — um humano conferiu a turma e
// libera pro bot de vendas falar dela.
func (s *Server) handlePortalLiberarTurmaBot(w http.ResponseWriter, r *http.Request) {
	s.setTurmaBotValidada(w, r, true)
}

// DELETE /portal/classes/{classId}/bot-validacao — tira a turma do bot.
func (s *Server) handlePortalRetirarTurmaBot(w http.ResponseWriter, r *http.Request) {
	s.setTurmaBotValidada(w, r, false)
}

func (s *Server) setTurmaBotValidada(w http.ResponseWriter, r *http.Request, liberar bool) {
	id, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), id); err != nil {
		writeErr(w, err)
		return
	}
	class, err := s.portalGetClass(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Turma"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if liberar {
		eventos, err := s.listAgendaEventos(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		if motivo := motivoTurmaNaoLiberavel(class, len(horariosDaTurma(eventos, id)), hojeNaEscola(time.Now())); motivo != "" {
			writeErr(w, appErr(http.StatusBadRequest, "TURMA_NAO_LIBERAVEL", motivo))
			return
		}
		_, err = s.portalDB.Exec(r.Context(),
			`UPDATE class SET bot_validada_em = NOW(), bot_validada_por = $2, updated_at = NOW() WHERE id = $1`, id, userIDFrom(r))
		if err != nil {
			writeErr(w, err)
			return
		}
	} else {
		if _, err := s.portalDB.Exec(r.Context(),
			`UPDATE class SET bot_validada_em = NULL, bot_validada_por = NULL, updated_at = NOW() WHERE id = $1`, id); err != nil {
			writeErr(w, err)
			return
		}
	}
	class, err = s.portalGetClass(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	acao := "class_bot_liberar"
	if !liberar {
		acao = "class_bot_retirar"
	}
	s.portalLogActivity(r, acao, "class", class.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"class": class})
}
