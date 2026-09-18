package main

import (
	"context"
	"errors"
	"fmt"
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
// Só múltipla escolha (type_exercise=1) — código e texto/dissertativo (0 e 2)
// passam pelo fluxo de correção manual do professor (portal_correcao) e nem
// aparecem aqui (filtrados na query, não só escondidos na UI).

// portalCurrentPhaseJoin resolve a fase ATUAL de cada matrícula do aluno — a
// primeira fase (menor id) do módulo em que a turma/aula está, mesma regra
// corrigida no portal .NET (GetCurrentPhaseModuleUserAsync). Extraído pra uma
// constante e reaproveitado em toda query deste arquivo de propósito: já
// divergiu uma vez entre a listagem e o access-check (um cadeado que só
// travava metade da porta), então só existe UM lugar que decide "qual é a
// fase atual" — impossível os dois se desalinharem de novo.
const portalCurrentPhaseJoin = `
	JOIN class c ON c.id = e.class_id
	JOIN phase ph ON ph.module_id = c.current_module_id
		AND ph.id = (SELECT MIN(ph2.id) FROM phase ph2 WHERE ph2.module_id = c.current_module_id)
`

type portalMyExerciseDTO struct {
	ID          string `json:"id"`
	PhaseID     string `json:"phaseId"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Type        int    `json:"type"`
	Points      int    `json:"pointsRedeem"`
	Answered    bool   `json:"answered"`
}

// portalMyExercises lista os exercícios de múltipla escolha da fase atual de
// cada matrícula do aluno. `answered` só é true quando TODAS as questões do
// exercício já têm resposta dele — antes contava qualquer linha em `answer`,
// o que marcava um exercício de 3 perguntas como "respondido" na primeira.
func (s *Server) portalMyExercises(ctx context.Context, portalUserID int64) ([]portalMyExerciseDTO, error) {
	rows, err := s.portalDB.Query(ctx, `
		SELECT id::text, phase_id::text, title, description, type_exercise, points_redeem, answered FROM (
			SELECT DISTINCT ex.id, ex.phase_id, COALESCE(ex.title,'') AS title, COALESCE(ex.description,'') AS description,
				COALESCE(ex.type_exercise,2) AS type_exercise, COALESCE(ex.points_redeem,0) AS points_redeem,
				EXISTS(SELECT 1 FROM question q0 WHERE q0.exercise_id = ex.id)
					AND (SELECT COUNT(DISTINCT a.question_id) FROM answer a WHERE a.exercise_id = ex.id AND a.user_id = $1)
						>= (SELECT COUNT(*) FROM question q1 WHERE q1.exercise_id = ex.id)
					AS answered
			FROM enrollment e
			`+portalCurrentPhaseJoin+`
			JOIN exercise ex ON ex.phase_id = ph.id
			WHERE e.user_id = $1
			  AND COALESCE(ex.type_exercise,2) = 1
		) t
		ORDER BY t.id ASC`, portalUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []portalMyExerciseDTO{}
	for rows.Next() {
		var dto portalMyExerciseDTO
		if err := rows.Scan(&dto.ID, &dto.PhaseID, &dto.Title, &dto.Description, &dto.Type, &dto.Points, &dto.Answered); err != nil {
			return nil, err
		}
		if dto.Title == "" {
			dto.Title = "Exercício " + dto.ID
		}
		items = append(items, dto)
	}
	return items, rows.Err()
}

// portalMyExerciseAccess confirma que a fase ATUAL de alguma matrícula do
// aluno é a fase do exercício — mesma junção de portalMyExercises (via
// portalCurrentPhaseJoin), e mesmo filtro de tipo. Sem o filtro de fase aqui
// (bug real encontrado em revisão: só checava o MÓDULO, não a fase), um
// aluno que soubesse o id de um exercício de uma fase futura do mesmo módulo
// conseguia abri-lo e respondê-lo antes de ser liberado.
func (s *Server) portalMyExerciseAccess(ctx context.Context, portalUserID, exerciseID int64) (bool, error) {
	var ok bool
	err := s.portalDB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM enrollment e
			`+portalCurrentPhaseJoin+`
			JOIN exercise ex ON ex.phase_id = ph.id
			WHERE e.user_id = $1 AND ex.id = $2 AND COALESCE(ex.type_exercise,2) = 1
		)`, portalUserID, exerciseID).Scan(&ok)
	return ok, err
}

// portalMyOptionDTO/portalMyQuestionDTO/portalMyExerciseDetailDTO: resposta do
// detalhe é MONTADA à mão (não reaproveita portalExerciseDTO/portalQuestionDTO
// diretamente na serialização) porque a regra de visibilidade é por questão:
// qual é a certa só aparece numa questão que o aluno JÁ respondeu — nas
// pendentes continua escondida, senão inspecionar a resposta de rede vira
// cola antes de tentar.
type portalMyOptionDTO struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Selected  bool   `json:"selected"`
	IsCorrect *bool  `json:"isCorrect,omitempty"`
}

type portalMyQuestionDTO struct {
	ID        string              `json:"id"`
	Statement string              `json:"statement"`
	Answered  bool                `json:"answered"`
	Options   []portalMyOptionDTO `json:"options"`
}

type portalMyExerciseDetailDTO struct {
	ID           string                `json:"id"`
	Title        string                `json:"title"`
	Description  string                `json:"description"`
	Type         int                   `json:"type"`
	PointsRedeem int                   `json:"pointsRedeem"`
	Questions    []portalMyQuestionDTO `json:"questions"`
}

// portalMyAnswersByQuestion devolve, por questão já respondida pelo aluno
// NESTE exercício, qual opção ele escolheu — usado tanto pra marcar
// `selected` quanto pra decidir em quais questões revelar `isCorrect`.
func (s *Server) portalMyAnswersByQuestion(ctx context.Context, portalUserID, exerciseID int64) (map[string]string, error) {
	rows, err := s.portalDB.Query(ctx, `
		SELECT question_id::text, selected_option::text
		FROM answer WHERE user_id = $1 AND exercise_id = $2`, portalUserID, exerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var q, o string
		if err := rows.Scan(&q, &o); err != nil {
			return nil, err
		}
		out[q] = o
	}
	return out, rows.Err()
}

// portalMyExerciseDetail monta a resposta do detalhe já com a visibilidade
// certa de gabarito por questão (ver comentário do DTO acima).
func (s *Server) portalMyExerciseDetail(ctx context.Context, portalUserID, exerciseID int64) (*portalMyExerciseDetailDTO, error) {
	dto, err := s.portalGetExercise(ctx, exerciseID)
	if err != nil {
		return nil, err
	}
	respondidas, err := s.portalMyAnswersByQuestion(ctx, portalUserID, exerciseID)
	if err != nil {
		return nil, err
	}
	out := &portalMyExerciseDetailDTO{
		ID: dto.ID, Title: dto.Title, Description: dto.Description,
		Type: dto.Type, PointsRedeem: dto.PointsRedeem,
	}
	for _, q := range dto.Questions {
		selecionada, respondida := respondidas[q.ID]
		mq := portalMyQuestionDTO{ID: q.ID, Statement: q.Statement, Answered: respondida}
		for _, o := range q.Options {
			mo := portalMyOptionDTO{ID: o.ID, Text: o.Text, Selected: respondida && o.ID == selecionada}
			if respondida {
				v := o.IsCorrect
				mo.IsCorrect = &v
			}
			mq.Options = append(mq.Options, mo)
		}
		out.Questions = append(out.Questions, mq)
	}
	return out, nil
}

type portalMySubmitInput struct {
	// Chegam como STRING no JSON — os DTOs de leitura já expõem id como
	// string (::text, pro front não perder precisão em número grande), e o
	// front devolve o mesmo valor que recebeu. Corpo com número quebraria o
	// decode.
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

// portalPointsGranted lê o crédito JÁ CONCEDIDO (portal_point, gravado por
// grantPointsForAnswers) pra esse aluno/exercício — 0 enquanto o exercício
// tiver questão pendente (grantPointsForAnswers só credita quando TODAS as
// questões do exercício estão corrigidas), sem estimar um valor que ainda
// não é real.
func (s *Server) portalPointsGranted(ctx context.Context, portalUserID, exerciseID int64) int {
	var points int
	_ = s.portalDB.QueryRow(ctx, `
		SELECT COALESCE(ROUND(points),0)::int FROM portal_point WHERE user_id=$1 AND exercise_id=$2`,
		portalUserID, exerciseID).Scan(&points)
	return points
}

// portalSubmitMyAnswer grava a resposta do aluno.
//
// Duas garantias que a v1 não tinha (achados de revisão):
//  1. A opção só é aceita se pertencer À QUESTÃO **e** a questão pertencer ao
//     EXERCÍCIO do path — sem isso, dava pra mandar o id de qualquer questão
//     do sistema (inclusive de curso em que não está matriculado) e usar o
//     `isCorrect` da resposta HTTP como oráculo de gabarito alheio.
//  2. Um advisory lock por (aluno, exercício, questão) serializa checagem +
//     insert — dois cliques/retries simultâneos não duplicam mais a linha em
//     `answer`. Não dá pra usar UNIQUE INDEX + ON CONFLICT porque a tabela
//     (schema legado, compartilhado com o app .NET) já tem duplicata
//     pré-existente de fora deste endpoint; um índice novo quebraria o INSERT
//     de quem quer que ainda grave lá.
func (s *Server) portalSubmitMyAnswer(ctx context.Context, portalUserID, exerciseID int64, in portalMySubmitInput) (*portalMySubmitResult, error) {
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op após Commit

	lockKey := fmt.Sprintf("answer:%d:%d:%d", portalUserID, exerciseID, in.questionID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return nil, err
	}

	var existing bool
	err = tx.QueryRow(ctx, `SELECT is_correct FROM answer WHERE user_id=$1 AND exercise_id=$2 AND question_id=$3`,
		portalUserID, exerciseID, in.questionID).Scan(&existing)
	if err == nil {
		return &portalMySubmitResult{IsCorrect: existing, PointsEarned: s.portalPointsGranted(ctx, portalUserID, exerciseID), AlreadyAnswered: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// A opção precisa pertencer à questão, e a questão precisar do EXERCÍCIO
	// do path — as três pontas amarradas na mesma query.
	var isCorrect bool
	if err := tx.QueryRow(ctx, `
		SELECT o.is_correct
		FROM question_option o
		JOIN question q ON q.id = o.question_id
		WHERE o.id = $1 AND o.question_id = $2 AND q.exercise_id = $3`,
		in.optionID, in.questionID, exerciseID).Scan(&isCorrect); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, validationErr("opção não encontrada para essa questão")
		}
		return nil, err
	}

	// user_exercise_flow_id tem DEFAULT 0, mas 0 não existe em
	// user_exercise_flow — o default do schema legado viola a própria FK
	// dele. NULL explícito (a coluna é nullable) evita o 23503.
	var newID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO answer (user_id, question_id, exercise_id, selected_option, is_correct, answered_at, user_exercise_flow_id)
		VALUES ($1, $2, $3, $4, $5, now(), NULL) RETURNING id`,
		portalUserID, in.questionID, exerciseID, in.optionID, isCorrect).Scan(&newID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Fora da transação, de propósito (mesmo padrão do fluxo de correção do
	// professor): grantPointsForAnswers é melhor-esforço e idempotente por
	// (user_id, reason) — só credita quando TODAS as questões do exercício
	// já estão corrigidas.
	s.grantPointsForAnswers(ctx, []int64{newID})

	return &portalMySubmitResult{IsCorrect: isCorrect, PointsEarned: s.portalPointsGranted(ctx, portalUserID, exerciseID)}, nil
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
	dto, err := s.portalMyExerciseDetail(r.Context(), portalUserID, exerciseID)
	if err != nil {
		writeErr(w, err)
		return
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
