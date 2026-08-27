package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santos-tech/auth/db"
)

type AgendaEvento struct {
	ID                         string    `json:"id"`
	Tipo                       string    `json:"tipo"`
	Titulo                     string    `json:"titulo"`
	AlunoOuGrupo               *string   `json:"alunoOuGrupo"`
	ProfessorOuResponsavelID   *int64    `json:"professorOuResponsavelId"`
	ProfessorOuResponsavelNome string    `json:"professorOuResponsavelNome"`
	Conteudo                   *string   `json:"conteudo"`
	Jogo                       *string   `json:"jogo"`
	QtdPessoas                 *int      `json:"qtdPessoas"`
	ComputadoresUsados         int       `json:"computadoresUsados"`
	DataInicio                 string    `json:"dataInicio"`
	HoraInicio                 string    `json:"horaInicio"`
	HoraFim                    string    `json:"horaFim"`
	Recorrencia                string    `json:"recorrencia"`
	DiaSemana                  *int      `json:"diaSemana"`
	DataFimRecorrencia         *string   `json:"dataFimRecorrencia"`
	StatusPreparo              *string   `json:"statusPreparo"`
	Notas                      string    `json:"notas"`
	CreatedBy                  *int64    `json:"createdBy"`
	CreatedByNome              string    `json:"createdByNome"`
	CreatedAt                  time.Time `json:"createdAt"`
	UpdatedAt                  time.Time `json:"updatedAt"`
}

type AgendaEventoInput struct {
	Tipo                     string  `json:"tipo"`
	Titulo                   string  `json:"titulo"`
	AlunoOuGrupo             *string `json:"alunoOuGrupo"`
	ProfessorOuResponsavelID *int64  `json:"professorOuResponsavelId"`
	Conteudo                 *string `json:"conteudo"`
	Jogo                     *string `json:"jogo"`
	QtdPessoas               *int    `json:"qtdPessoas"`
	ComputadoresUsados       int     `json:"computadoresUsados"`
	DataInicio               string  `json:"dataInicio"`
	HoraInicio               string  `json:"horaInicio"`
	HoraFim                  string  `json:"horaFim"`
	Recorrencia              string  `json:"recorrencia"`
	DiaSemana                *int    `json:"diaSemana"`
	DataFimRecorrencia       *string `json:"dataFimRecorrencia"`
	StatusPreparo            *string `json:"statusPreparo"`
	Notas                    string  `json:"notas"`
	// ConfirmarConflito: reenvio do form depois do usuário confirmar o aviso
	// de conflito de política (409 AGENDA_CONFLITO_POLITICA). Nunca sobrepõe
	// o bloqueio de capacidade (400 AGENDA_CAPACIDADE_EXCEDIDA), que não tem
	// override.
	ConfirmarConflito bool `json:"confirmarConflito"`
}

// toEvento monta um AgendaEvento em memória a partir do input, SEM tocar o
// banco — usado pra rodar checkConflitos antes de decidir se grava.
// id="" para criação; passar o id existente numa edição para que
// checkConflitos ignore o próprio evento ao comparar.
func (in AgendaEventoInput) toEvento(id string) AgendaEvento {
	return AgendaEvento{
		ID: id, Tipo: in.Tipo, Titulo: in.Titulo, AlunoOuGrupo: in.AlunoOuGrupo,
		ProfessorOuResponsavelID: in.ProfessorOuResponsavelID, Conteudo: in.Conteudo,
		Jogo: in.Jogo, QtdPessoas: in.QtdPessoas, ComputadoresUsados: in.ComputadoresUsados,
		DataInicio: in.DataInicio, HoraInicio: in.HoraInicio, HoraFim: in.HoraFim,
		Recorrencia: in.Recorrencia, DiaSemana: in.DiaSemana,
		DataFimRecorrencia: in.DataFimRecorrencia, StatusPreparo: in.StatusPreparo, Notas: in.Notas,
	}
}

var validAgendaTipos = map[string]bool{
	"aula_turma": true, "aula_particular": true, "aula_experimental": true,
	"avulso": true, "corujao": true, "mix": true,
}
var validAgendaStatusPreparo = map[string]bool{"nao_aplica": true, "pendente": true, "pronto": true}

// ── Conversões pgtype ↔ domínio ─────────────────────────────────────────────

func pgInt4ToInt64Ptr(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	x := int64(v.Int32)
	return &x
}

func pgInt4ToIntPtr(v pgtype.Int4) *int {
	if !v.Valid {
		return nil
	}
	x := int(v.Int32)
	return &x
}

func int64PtrToPgInt4(v *int64) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

func intPtrToPgInt4(v *int) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

func int16PtrToIntPtr(v *int16) *int {
	if v == nil {
		return nil
	}
	x := int(*v)
	return &x
}

func intPtrToInt16Ptr(v *int) *int16 {
	if v == nil {
		return nil
	}
	x := int16(*v)
	return &x
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func dateStrToPgDate(s *string) pgtype.Date {
	if s == nil || *s == "" {
		return pgtype.Date{}
	}
	t, err := time.Parse("2006-01-02", *s)
	if err != nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

func timeStrToPgTime(s string) pgtype.Time {
	t, err := time.Parse("15:04:05", s)
	if err != nil {
		t, err = time.Parse("15:04", s)
		if err != nil {
			return pgtype.Time{}
		}
	}
	micros := int64(t.Hour())*3600e6 + int64(t.Minute())*60e6 + int64(t.Second())*1e6
	return pgtype.Time{Microseconds: micros, Valid: true}
}

// mkAgendaEvento monta o domínio a partir dos campos crus de qualquer uma das
// 4 Row structs geradas pelo sqlc (List/Get/Insert/Update) — todas têm a
// mesma forma, mas são tipos Go distintos, então cada call site desempacota
// os próprios campos aqui.
func mkAgendaEvento(
	id, tipo, titulo string, alunoOuGrupo *string,
	professorID pgtype.Int4, professorNome string,
	conteudo, jogo *string, qtdPessoas pgtype.Int4, pcs int32,
	dataInicio, horaInicio, horaFim, recorrencia string, diaSemana *int16, dataFimRecorrencia string,
	statusPreparo *string, notas string, createdBy pgtype.Int4, createdByNome string,
	createdAt, updatedAt pgtype.Timestamptz,
) AgendaEvento {
	return AgendaEvento{
		ID: id, Tipo: tipo, Titulo: titulo, AlunoOuGrupo: alunoOuGrupo,
		ProfessorOuResponsavelID: pgInt4ToInt64Ptr(professorID), ProfessorOuResponsavelNome: professorNome,
		Conteudo: conteudo, Jogo: jogo, QtdPessoas: pgInt4ToIntPtr(qtdPessoas), ComputadoresUsados: int(pcs),
		DataInicio: dataInicio, HoraInicio: horaInicio, HoraFim: horaFim,
		Recorrencia: recorrencia, DiaSemana: int16PtrToIntPtr(diaSemana), DataFimRecorrencia: strPtrOrNil(dataFimRecorrencia),
		StatusPreparo: statusPreparo, Notas: notas,
		CreatedBy: pgInt4ToInt64Ptr(createdBy), CreatedByNome: createdByNome,
		CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time,
	}
}

func agendaEventoFromList(r db.ListAgendaEventosRow) AgendaEvento {
	return mkAgendaEvento(r.ID, r.Tipo, r.Titulo, r.AlunoOuGrupo, r.ProfessorOuResponsavelID, r.ProfessorOuResponsavelNome,
		r.Conteudo, r.Jogo, r.QtdPessoas, r.ComputadoresUsados, r.DataInicio, r.HoraInicio, r.HoraFim,
		r.Recorrencia, r.DiaSemana, r.DataFimRecorrencia, r.StatusPreparo, r.Notas, r.CreatedBy, r.CreatedByNome,
		r.CreatedAt, r.UpdatedAt)
}

func agendaEventoFromGet(r db.GetAgendaEventoRow) AgendaEvento {
	return mkAgendaEvento(r.ID, r.Tipo, r.Titulo, r.AlunoOuGrupo, r.ProfessorOuResponsavelID, r.ProfessorOuResponsavelNome,
		r.Conteudo, r.Jogo, r.QtdPessoas, r.ComputadoresUsados, r.DataInicio, r.HoraInicio, r.HoraFim,
		r.Recorrencia, r.DiaSemana, r.DataFimRecorrencia, r.StatusPreparo, r.Notas, r.CreatedBy, r.CreatedByNome,
		r.CreatedAt, r.UpdatedAt)
}

func agendaEventoFromInsert(r db.InsertAgendaEventoRow) AgendaEvento {
	return mkAgendaEvento(r.ID, r.Tipo, r.Titulo, r.AlunoOuGrupo, r.ProfessorOuResponsavelID, r.ProfessorOuResponsavelNome,
		r.Conteudo, r.Jogo, r.QtdPessoas, r.ComputadoresUsados, r.DataInicio, r.HoraInicio, r.HoraFim,
		r.Recorrencia, r.DiaSemana, r.DataFimRecorrencia, r.StatusPreparo, r.Notas, r.CreatedBy, r.CreatedByNome,
		r.CreatedAt, r.UpdatedAt)
}

func agendaEventoFromUpdate(r db.UpdateAgendaEventoRow) AgendaEvento {
	return mkAgendaEvento(r.ID, r.Tipo, r.Titulo, r.AlunoOuGrupo, r.ProfessorOuResponsavelID, r.ProfessorOuResponsavelNome,
		r.Conteudo, r.Jogo, r.QtdPessoas, r.ComputadoresUsados, r.DataInicio, r.HoraInicio, r.HoraFim,
		r.Recorrencia, r.DiaSemana, r.DataFimRecorrencia, r.StatusPreparo, r.Notas, r.CreatedBy, r.CreatedByNome,
		r.CreatedAt, r.UpdatedAt)
}

func uuidToPg(id string) pgtype.UUID {
	var u pgtype.UUID
	_ = u.Scan(id)
	return u
}

// ── Server methods ───────────────────────────────────────────────────────

// listAgendaEventos: sem paginação/filtro, mesmo padrão de listSocialPosts/
// listTasks — volume de eventos de uma escola é baixo o bastante pra isso
// nunca ser um problema. O front resolve ocorrências/janela de exibição.
func (s *Server) listAgendaEventos(ctx context.Context) ([]AgendaEvento, error) {
	rows, err := s.q.ListAgendaEventos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AgendaEvento, len(rows))
	for i, r := range rows {
		out[i] = agendaEventoFromList(r)
	}
	return out, nil
}

func (s *Server) getAgendaEvento(ctx context.Context, id string) (*AgendaEvento, error) {
	r, err := s.q.GetAgendaEvento(ctx, uuidToPg(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ev := agendaEventoFromGet(r)
	return &ev, nil
}

func (s *Server) insertAgendaEvento(ctx context.Context, in AgendaEventoInput, createdBy int64) (*AgendaEvento, error) {
	r, err := s.q.InsertAgendaEvento(ctx, db.InsertAgendaEventoParams{
		Tipo: in.Tipo, Titulo: in.Titulo, AlunoOuGrupo: in.AlunoOuGrupo,
		ProfessorOuResponsavelID: int64PtrToPgInt4(in.ProfessorOuResponsavelID),
		Conteudo:                 in.Conteudo, Jogo: in.Jogo, QtdPessoas: intPtrToPgInt4(in.QtdPessoas),
		ComputadoresUsados: int32(in.ComputadoresUsados),
		DataInicio:         dateStrToPgDate(&in.DataInicio), HoraInicio: timeStrToPgTime(in.HoraInicio), HoraFim: timeStrToPgTime(in.HoraFim),
		Recorrencia: in.Recorrencia, DiaSemana: intPtrToInt16Ptr(in.DiaSemana), DataFimRecorrencia: dateStrToPgDate(in.DataFimRecorrencia),
		StatusPreparo: in.StatusPreparo, Notas: in.Notas, CreatedBy: pgtype.Int4{Int32: int32(createdBy), Valid: true},
	})
	if err != nil {
		return nil, portalDBErr(err)
	}
	ev := agendaEventoFromInsert(r)
	return &ev, nil
}

func (s *Server) updateAgendaEvento(ctx context.Context, id string, in AgendaEventoInput) (*AgendaEvento, error) {
	r, err := s.q.UpdateAgendaEvento(ctx, db.UpdateAgendaEventoParams{
		ID: uuidToPg(id), Tipo: in.Tipo, Titulo: in.Titulo, AlunoOuGrupo: in.AlunoOuGrupo,
		ProfessorOuResponsavelID: int64PtrToPgInt4(in.ProfessorOuResponsavelID),
		Conteudo:                 in.Conteudo, Jogo: in.Jogo, QtdPessoas: intPtrToPgInt4(in.QtdPessoas),
		ComputadoresUsados: int32(in.ComputadoresUsados),
		DataInicio:         dateStrToPgDate(&in.DataInicio), HoraInicio: timeStrToPgTime(in.HoraInicio), HoraFim: timeStrToPgTime(in.HoraFim),
		Recorrencia: in.Recorrencia, DiaSemana: intPtrToInt16Ptr(in.DiaSemana), DataFimRecorrencia: dateStrToPgDate(in.DataFimRecorrencia),
		StatusPreparo: in.StatusPreparo, Notas: in.Notas,
	})
	if err != nil {
		return nil, portalDBErr(err)
	}
	ev := agendaEventoFromUpdate(r)
	return &ev, nil
}

func (s *Server) deleteAgendaEvento(ctx context.Context, id string) error {
	n, err := s.q.DeleteAgendaEvento(ctx, uuidToPg(id))
	if err != nil {
		return err
	}
	if n == 0 {
		return errAgendaEventoNotFound
	}
	return nil
}

func (s *Server) insertAgendaEventoConfirmacao(ctx context.Context, eventoID string, userID int64, conflitosComIDs []string) error {
	return s.q.InsertAgendaEventoConfirmacao(ctx, db.InsertAgendaEventoConfirmacaoParams{
		EventoID: uuidToPg(eventoID), UserID: pgtype.Int4{Int32: int32(userID), Valid: true},
		ConflitosComIds: conflitosComIDs,
	})
}

// ── Feriados municipais (CRUD admin-only) ───────────────────────────────────

type AgendaFeriadoMunicipal struct {
	ID        int64     `json:"id"`
	Data      string    `json:"data"`
	Nome      string    `json:"nome"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) listAgendaFeriadosMunicipaisAno(ctx context.Context, ano int) ([]AgendaFeriado, error) {
	rows, err := s.q.ListAgendaFeriadosMunicipaisAno(ctx, int32(ano))
	if err != nil {
		return nil, err
	}
	out := make([]AgendaFeriado, len(rows))
	for i, r := range rows {
		out[i] = AgendaFeriado{Data: r.Data, Nome: r.Nome, Tipo: "municipal"}
	}
	return out, nil
}

func (s *Server) listAgendaFeriadosMunicipais(ctx context.Context) ([]AgendaFeriadoMunicipal, error) {
	rows, err := s.q.ListAgendaFeriadosMunicipais(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AgendaFeriadoMunicipal, len(rows))
	for i, r := range rows {
		out[i] = AgendaFeriadoMunicipal{ID: int64(r.ID), Data: r.Data, Nome: r.Nome, CreatedAt: r.CreatedAt.Time}
	}
	return out, nil
}

func (s *Server) insertAgendaFeriadoMunicipal(ctx context.Context, data, nome string) (*AgendaFeriadoMunicipal, error) {
	r, err := s.q.InsertAgendaFeriadoMunicipal(ctx, db.InsertAgendaFeriadoMunicipalParams{
		Data: dateStrToPgDate(&data), Nome: nome,
	})
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &AgendaFeriadoMunicipal{ID: int64(r.ID), Data: r.Data, Nome: r.Nome, CreatedAt: r.CreatedAt.Time}, nil
}

func (s *Server) deleteAgendaFeriadoMunicipal(ctx context.Context, id int64) error {
	n, err := s.q.DeleteAgendaFeriadoMunicipal(ctx, int32(id))
	if err != nil {
		return err
	}
	if n == 0 {
		return errAgendaFeriadoNotFound
	}
	return nil
}
