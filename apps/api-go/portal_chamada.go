package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Chamada: gerar as aulas de uma turma a partir da grade semanal e registrar
// presença/falta por aluno em cada uma.

type portalSessionDTO struct {
	ID        string                `json:"id"`
	ClassID   string                `json:"classId"`
	Date      string                `json:"date"` // AAAA-MM-DD
	StartTime string                `json:"startTime,omitempty"`
	EndTime   string                `json:"endTime,omitempty"`
	Canceled  bool                  `json:"canceled"`
	Note      string                `json:"note,omitempty"`
	Presencas []portalAttendanceDTO `json:"presencas"`
}

type portalAttendanceDTO struct {
	UserID    string `json:"userId"`
	UserName  string `json:"userName"`
	UserEmail string `json:"userEmail"`
	Status    string `json:"status,omitempty"` // vazio = chamada não feita
	Note      string `json:"note,omitempty"`
}

var portalStatusChamada = map[string]bool{"presente": true, "falta": true, "justificada": true}

// portalGenerateSessions materializa as aulas entre duas datas a partir da
// grade semanal da turma. Idempotente: ON CONFLICT DO NOTHING no índice
// (class_id, date, start_time), então rodar de novo não duplica nem apaga
// chamada já feita.
func (s *Server) portalGenerateSessions(ctx context.Context, classID int64, de, ate time.Time) (int, error) {
	grade, err := s.portalListClassSchedule(ctx, classID)
	if err != nil {
		return 0, err
	}
	if len(grade) == 0 {
		return 0, appErr(400, "SEM_GRADE", "Esta turma não tem horário semanal cadastrado — defina a grade antes de gerar as aulas")
	}
	if ate.Before(de) {
		return 0, validationErr("data final antes da inicial")
	}
	if ate.Sub(de) > 400*24*time.Hour {
		return 0, validationErr("intervalo maior que um ano")
	}

	criadas := 0
	for dia := de; !dia.After(ate); dia = dia.AddDate(0, 0, 1) {
		for _, h := range grade {
			if int16(dia.Weekday()) != h.DayOfWeek {
				continue
			}
			tag, err := s.portalDB.Exec(ctx,
				`INSERT INTO class_session (class_id, date, start_time, end_time, created_at, updated_at)
				 VALUES ($1,$2,$3::time,$4::time,NOW(),NOW())
				 ON CONFLICT (class_id, date, start_time) DO NOTHING`,
				classID, dia.Format("2006-01-02"), h.StartTime, h.EndTime)
			if err != nil {
				return criadas, err
			}
			criadas += int(tag.RowsAffected())
		}
	}
	return criadas, nil
}

// portalListSessions traz as aulas da turma com a chamada de cada aluno
// matriculado. Aluno sem linha em attendance aparece com status vazio — é a
// diferença entre "faltou" e "ninguém fez a chamada ainda".
func (s *Server) portalListSessions(ctx context.Context, classID int64) ([]portalSessionDTO, error) {
	rows, err := s.portalDB.Query(ctx,
		`SELECT id::text, class_id::text, to_char(date,'YYYY-MM-DD'),
		        COALESCE(to_char(start_time,'HH24:MI'),''), COALESCE(to_char(end_time,'HH24:MI'),''),
		        canceled, COALESCE(note,'')
		 FROM class_session WHERE class_id=$1 ORDER BY date DESC, start_time`, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessoes := []portalSessionDTO{}
	idx := map[string]int{}
	for rows.Next() {
		var d portalSessionDTO
		if err := rows.Scan(&d.ID, &d.ClassID, &d.Date, &d.StartTime, &d.EndTime, &d.Canceled, &d.Note); err != nil {
			return nil, err
		}
		d.Presencas = []portalAttendanceDTO{}
		idx[d.ID] = len(sessoes)
		sessoes = append(sessoes, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(sessoes) == 0 {
		return sessoes, nil
	}

	// Produto cartesiano aluno × aula em UMA query: com a chamada por aula
	// numa query cada, uma turma com 40 aulas viraria 40 idas ao banco.
	pres, err := s.portalDB.Query(ctx,
		`SELECT cs.id::text, u.id::text, COALESCE(u.name,''), COALESCE(u.email,''),
		        COALESCE(a.status,''), COALESCE(a.note,'')
		 FROM class_session cs
		 JOIN enrollment e ON e.class_id = cs.class_id
		 JOIN "user" u ON u.id = e.user_id
		 LEFT JOIN attendance a ON a.session_id = cs.id AND a.user_id = u.id
		 WHERE cs.class_id = $1
		 ORDER BY u.name`, classID)
	if err != nil {
		return nil, err
	}
	defer pres.Close()
	for pres.Next() {
		var sid string
		var a portalAttendanceDTO
		if err := pres.Scan(&sid, &a.UserID, &a.UserName, &a.UserEmail, &a.Status, &a.Note); err != nil {
			return nil, err
		}
		if i, ok := idx[sid]; ok {
			sessoes[i].Presencas = append(sessoes[i].Presencas, a)
		}
	}
	return sessoes, pres.Err()
}

// portalSetAttendance grava (ou regrava) a presença de um aluno numa aula.
func (s *Server) portalSetAttendance(ctx context.Context, sessionID, userID int64, status, nota string) error {
	status = strings.TrimSpace(strings.ToLower(status))
	if !portalStatusChamada[status] {
		return validationErr("status deve ser presente, falta ou justificada")
	}
	var classID int64
	err := s.portalDB.QueryRow(ctx, `SELECT class_id FROM class_session WHERE id=$1`, sessionID).Scan(&classID)
	if errors.Is(err, pgx.ErrNoRows) {
		return appErr(404, "SESSION_NOT_FOUND", "Aula não encontrada")
	}
	if err != nil {
		return err
	}
	// O aluno tem que estar matriculado NESTA turma: sem isso dava pra gravar
	// falta de qualquer usuário do sistema numa aula que não é dele.
	var matriculado bool
	if err := s.portalDB.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM enrollment WHERE class_id=$1 AND user_id=$2)`, classID, userID).Scan(&matriculado); err != nil {
		return err
	}
	if !matriculado {
		return appErr(400, "NAO_MATRICULADO", "Este aluno não está matriculado na turma desta aula")
	}
	_, err = s.portalDB.Exec(ctx,
		`INSERT INTO attendance (session_id, user_id, status, note, created_at, updated_at)
		 VALUES ($1,$2,$3,NULLIF($4,''),NOW(),NOW())
		 ON CONFLICT (session_id, user_id)
		 DO UPDATE SET status=EXCLUDED.status, note=EXCLUDED.note, updated_at=NOW()`,
		sessionID, userID, status, nota)
	return err
}

// portalFaltasDoAluno conta faltas (não justificadas) por aluno numa turma.
func (s *Server) portalFaltasDoAluno(ctx context.Context, classID, userID int64) (faltas, total int, err error) {
	err = s.portalDB.QueryRow(ctx,
		`SELECT COUNT(*) FILTER (WHERE a.status = 'falta'), COUNT(*)
		 FROM class_session cs
		 LEFT JOIN attendance a ON a.session_id = cs.id AND a.user_id = $2
		 WHERE cs.class_id = $1 AND NOT cs.canceled`, classID, userID).Scan(&faltas, &total)
	return
}

// portalGerarAulasDeTodasAsTurmas materializa as aulas recentes de TODA turma
// que tenha grade semanal. É o que o cron chama diariamente pra a chamada
// aparecer sozinha, sem ninguém clicar em "Gerar aulas".
//
// Janela curta (últimos dias, não desde o início da turma) por dois motivos:
// o custo diário fica constante em vez de crescer com a idade da turma, e uns
// dias de folga cobrem um cron que falhou ontem sem precisar de estado. Puxar
// histórico antigo continua sendo trabalho do botão manual, que é onde alguém
// escolhe conscientemente a data de início.
func (s *Server) portalGerarAulasDeTodasAsTurmas(ctx context.Context, diasParaTras int) (turmas, criadas int, err error) {
	rows, err := s.portalDB.Query(ctx,
		`SELECT DISTINCT c.id, c.start_date FROM class c
		 JOIN class_schedule cs ON cs.class_id = c.id`)
	if err != nil {
		return 0, 0, err
	}
	type alvo struct {
		id     int64
		inicio time.Time
	}
	var alvos []alvo
	for rows.Next() {
		var a alvo
		if err := rows.Scan(&a.id, &a.inicio); err != nil {
			rows.Close()
			return 0, 0, err
		}
		alvos = append(alvos, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	hoje := time.Now()
	de := hoje.AddDate(0, 0, -diasParaTras)
	for _, a := range alvos {
		// Nunca antes do início da turma: gerar aula de antes de a turma
		// existir criaria falta de um período em que o aluno nem estudava.
		inicio := de
		if a.inicio.After(inicio) {
			inicio = a.inicio
		}
		if inicio.After(hoje) {
			continue // turma que ainda vai começar
		}
		n, err := s.portalGenerateSessions(ctx, a.id, inicio, hoje)
		if err != nil {
			// Uma turma com problema não pode impedir as outras de rodar.
			slog.Error("chamada: falha ao gerar aulas da turma", "class", a.id, "err", err)
			continue
		}
		criadas += n
	}
	return len(alvos), criadas, nil
}
