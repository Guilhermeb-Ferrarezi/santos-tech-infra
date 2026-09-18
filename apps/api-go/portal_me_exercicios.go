package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// ── Exercícios do aluno (autosserviço) ──────────────────────────────────────
//
// Diferente do CRUD de exercício (admin, portal_content_store.go/ExercicioDialog
// no dashboard), aqui é o próprio aluno respondendo o que já existe. Curso/
// módulo/fase é conteúdo do CURSO, não da matrícula — por isso funciona igual
// pra turma e aula particular: a modalidade só decide QUEM está na classe, não
// QUAL exercício aparece (ver TurmaDialog.tsx no dashboard).
//
// Só cobre múltipla escolha (type_exercise=1) por enquanto — é o que tem
// pergunta/opção pra responder direto. Código e texto/dissertativo (0 e 2)
// passam pelo fluxo de correção manual do professor (portal_correcao), fora
// de escopo aqui.

type portalMyExerciseDTO struct {
	ID       string `json:"id"`
	PhaseID  string `json:"phaseId"`
	Title    string `json:"title"`
	Type     int    `json:"type"`
	Points   int    `json:"pointsRedeem"`
	Answered bool   `json:"answered"`
}

// portalMyExercises lista os exercícios da fase ATUAL de cada matrícula do
// aluno — a primeira fase (menor id) do módulo em que a turma/aula está,
// igual à regra que corrigimos no portal .NET (GetCurrentPhaseModuleUserAsync).
func (s *Server) portalMyExercises(ctx context.Context, portalUserID int64) ([]portalMyExerciseDTO, error) {
	// DISTINCT + ORDER BY precisam da mesma expressão no Postgres (42P10) — o
	// cast ::text no SELECT e o "ex.id" cru no ORDER BY são expressões
	// diferentes pra esse fim. Subquery evita o problema (e mantém a
	// ordenação numérica certa, não lexicográfica de string).
	rows, err := s.portalDB.Query(ctx, `
		SELECT id::text, phase_id::text, title, type_exercise, points_redeem, answered FROM (
			SELECT DISTINCT ex.id, ex.phase_id, COALESCE(ex.title,'') AS title,
				COALESCE(ex.type_exercise,2) AS type_exercise, COALESCE(ex.points_redeem,0) AS points_redeem,
				EXISTS(SELECT 1 FROM answer a WHERE a.exercise_id = ex.id AND a.user_id = $1) AS answered
			FROM enrollment e
			JOIN class c ON c.id = e.class_id
			JOIN phase ph ON ph.module_id = c.current_module_id
			JOIN exercise ex ON ex.phase_id = ph.id
			WHERE e.user_id = $1
			  AND ph.id = (SELECT MIN(ph2.id) FROM phase ph2 WHERE ph2.module_id = c.current_module_id)
		) t
		ORDER BY t.id ASC`, portalUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []portalMyExerciseDTO{}
	for rows.Next() {
		var dto portalMyExerciseDTO
		if err := rows.Scan(&dto.ID, &dto.PhaseID, &dto.Title, &dto.Type, &dto.Points, &dto.Answered); err != nil {
			return nil, err
		}
		if dto.Title == "" {
			dto.Title = "Exercício " + dto.ID
		}
		items = append(items, dto)
	}
	return items, rows.Err()
}

// portalMyExerciseAccess confirma que o aluno está matriculado num curso cuja
// fase atual é a do exercício, antes de devolver questão ou aceitar resposta
// — sem isso qualquer aluno logado poderia ler/responder o exercício de
// qualquer curso só sabendo o id.
func (s *Server) portalMyExerciseAccess(ctx context.Context, portalUserID, exerciseID int64) (bool, error) {
	var ok bool
	err := s.portalDB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM enrollment e
			JOIN class c ON c.id = e.class_id
			JOIN phase ph ON ph.module_id = c.current_module_id
			JOIN exercise ex ON ex.phase_id = ph.id
			WHERE e.user_id = $1 AND ex.id = $2
		)`, portalUserID, exerciseID).Scan(&ok)
	return ok, err
}

// QuestionID/OptionID chegam como STRING no JSON — os DTOs de leitura
// (portalQuestionDTO/portalOptionDTO) já expõem id como string (::text, pro
// front não perder precisão em número grande), e o front devolve o mesmo
// valor que recebeu. Corpo com número aqui quebraria o decode.
type portalMySubmitInput struct {
	QuestionID string `json:"questionId"`
	OptionID   string `json:"optionId"`
	questionID int64
	optionID   int64
}

func (in *portalMySubmitInput) validate() error {
	var err error
	if in.questionID, err = strconv.ParseInt(in.QuestionID, 10, 64); err != nil || in.questionID <= 0 {
		return validationErr("questionId inválido")
	}
	if in.optionID, err = strconv.ParseInt(in.OptionID, 10, 64); err != nil || in.optionID <= 0 {
		return validationErr("optionId inválido")
	}
	return nil
}

type portalMySubmitResult struct {
	IsCorrect       bool `json:"isCorrect"`
	PointsEarned    int  `json:"pointsEarned"`
	AlreadyAnswered bool `json:"alreadyAnswered"`
}

// portalSubmitMyAnswer grava a resposta do aluno. Idempotente: responder de
// novo a mesma questão devolve o resultado já salvo, sem duplicar linha em
// `answer` nem deixar o aluno "tentar de novo até acertar".
func (s *Server) portalSubmitMyAnswer(ctx context.Context, portalUserID, exerciseID int64, in portalMySubmitInput) (*portalMySubmitResult, error) {
	var existing bool
	err := s.portalDB.QueryRow(ctx, `SELECT is_correct FROM answer WHERE user_id=$1 AND exercise_id=$2 AND question_id=$3`,
		portalUserID, exerciseID, in.questionID).Scan(&existing)
	if err == nil {
		points := 0
		if existing {
			_ = s.portalDB.QueryRow(ctx, `SELECT COALESCE(points_redeem,0) FROM exercise WHERE id=$1`, exerciseID).Scan(&points)
		}
		return &portalMySubmitResult{IsCorrect: existing, PointsEarned: points, AlreadyAnswered: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	var isCorrect bool
	if err := s.portalDB.QueryRow(ctx, `SELECT is_correct FROM question_option WHERE id=$1 AND question_id=$2`,
		in.optionID, in.questionID).Scan(&isCorrect); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, validationErr("opção não encontrada para essa questão")
		}
		return nil, err
	}

	if _, err := s.portalDB.Exec(ctx, `
		INSERT INTO answer (user_id, question_id, exercise_id, selected_option, is_correct, answered_at)
		VALUES ($1, $2, $3, $4, $5, now())`,
		portalUserID, in.questionID, exerciseID, in.optionID, isCorrect); err != nil {
		return nil, err
	}

	points := 0
	if isCorrect {
		_ = s.portalDB.QueryRow(ctx, `SELECT COALESCE(points_redeem,0) FROM exercise WHERE id=$1`, exerciseID).Scan(&points)
	}
	return &portalMySubmitResult{IsCorrect: isCorrect, PointsEarned: points}, nil
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handlePortalMyExercises(w http.ResponseWriter, r *http.Request) {
	u := s.portalUserFromSession(w, r)
	if u == nil {
		return
	}
	portalUserID, ok, err := s.posaulaPortalUserID(r.Context(), u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"exercises": []portalMyExerciseDTO{}})
		return
	}
	items, err := s.portalMyExercises(r.Context(), portalUserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exercises": items})
}

func (s *Server) handlePortalMyExerciseDetail(w http.ResponseWriter, r *http.Request) {
	exerciseID, err := portalPathID(r, "exerciseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	u := s.portalUserFromSession(w, r)
	if u == nil {
		return
	}
	portalUserID, ok, err := s.posaulaPortalUserID(r.Context(), u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, notFoundErr("Exercício"))
		return
	}
	access, err := s.portalMyExerciseAccess(r.Context(), portalUserID, exerciseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !access {
		writeErr(w, notFoundErr("Exercício"))
		return
	}
	dto, err := s.portalGetExercise(r.Context(), exerciseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Esconde qual opção é a correta antes de o aluno responder.
	for qi := range dto.Questions {
		for oi := range dto.Questions[qi].Options {
			dto.Questions[qi].Options[oi].IsCorrect = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"exercise": dto})
}

func (s *Server) handlePortalMyExerciseAnswer(w http.ResponseWriter, r *http.Request) {
	exerciseID, err := portalPathID(r, "exerciseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalMySubmitInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	u := s.portalUserFromSession(w, r)
	if u == nil {
		return
	}
	portalUserID, ok, err := s.posaulaPortalUserID(r.Context(), u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, notFoundErr("Exercício"))
		return
	}
	access, err := s.portalMyExerciseAccess(r.Context(), portalUserID, exerciseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !access {
		writeErr(w, notFoundErr("Exercício"))
		return
	}
	result, err := s.portalSubmitMyAnswer(r.Context(), portalUserID, exerciseID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
