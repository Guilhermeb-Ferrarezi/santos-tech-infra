package main

import (
	"context"
	"errors"
	"net/http"

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
