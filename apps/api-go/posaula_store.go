package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Pós-aula — DTOs e consultas das práticas (tabelas posaula_task e
// posaula_answer, ver portal_migrate.go). O JSON daqui é o contrato com o
// dashboard (web/src/lib/portal/pratica.ts): mudar nome de campo quebra a
// tela do aluno e a do professor ao mesmo tempo.

// ── DTOs ─────────────────────────────────────────────────────────────────────

// posaulaRespostaDTO é a resposta do aluno como ELE a vê. correctOption só
// existe depois de responder (mc) — antes disso o gabarito nunca sai da API.
type posaulaRespostaDTO struct {
	AnswerText     *string   `json:"answerText"`
	SelectedOption *int      `json:"selectedOption"`
	IsCorrect      *bool     `json:"isCorrect"`
	Feedback       *string   `json:"feedback"`
	CorrectOption  *int      `json:"correctOption"`
	AnsweredAt     time.Time `json:"answeredAt"`
}

// posaulaPraticaDTO é uma prática na tela do ALUNO. Sem answerKey: o campo
// simplesmente não existe neste DTO, então não tem como vazar por engano.
type posaulaPraticaDTO struct {
	ID            string              `json:"id"`
	SessionID     string              `json:"sessionId"`
	SessionDate   string              `json:"sessionDate"` // aaaa-mm-dd
	ClassID       string              `json:"classId"`
	ClassName     string              `json:"className"`
	Title         string              `json:"title"`
	Statement     string              `json:"statement"`
	Kind          string              `json:"kind"`
	Options       []string            `json:"options"`
	Hint          string              `json:"hint"`
	Difficulty    string              `json:"difficulty"`
	AvailableFrom time.Time           `json:"availableFrom"`
	Answer        *posaulaRespostaDTO `json:"answer"`
}

// posaulaPraticaProfessorDTO é a mesma prática na tela do PROFESSOR: com
// gabarito, com o aluno de quem ela é e — depois que o aluno respondeu —
// com a resposta e o retorno, pra ele auditar a correção do Claude ou
// olhar a aberta que caiu no fallback (correctedAt preenchido com
// isCorrect null = "aguardando o professor").
type posaulaPraticaProfessorDTO struct {
	ID            string     `json:"id"`
	StudentID     string     `json:"studentId"`
	StudentName   string     `json:"studentName"`
	Title         string     `json:"title"`
	Statement     string     `json:"statement"`
	Kind          string     `json:"kind"`
	Options       []string   `json:"options"`
	AnswerKey     string     `json:"answerKey"`
	Hint          string     `json:"hint"`
	Difficulty    string     `json:"difficulty"`
	AvailableFrom time.Time  `json:"availableFrom"`
	Answered      bool       `json:"answered"`
	IsCorrect     *bool      `json:"isCorrect"`
	AnswerText    *string    `json:"answerText"`
	Feedback      *string    `json:"feedback"`
	CorrectedAt   *time.Time `json:"correctedAt"`
}

// posaulaPraticasDaAulaDTO — GET /portal/sessions/{id}/tasks. aiStatus/aiError
// vêm do diário (null quando a aula ainda não tem diário), já passados por
// posaulaStatusEfetivo.
type posaulaPraticasDaAulaDTO struct {
	Tasks    []posaulaPraticaProfessorDTO `json:"tasks"`
	AiStatus *string                      `json:"aiStatus"`
	AiError  *string                      `json:"aiError"`
}

// ── Estado efetivo da geração ────────────────────────────────────────────────

const (
	// posaulaGeracaoExpiraEm: pending/running mais velho que isto não tem
	// mais task viva por trás (o Timeout do asynq é 10 min; o processo pode
	// ter morrido entre marcar pending e enfileirar; o Redis pode ter sido
	// limpo). A tela trata como falha, com o botão de gerar de novo.
	posaulaGeracaoExpiraEm = 20 * time.Minute
	posaulaErroExpirou     = "A geração expirou. Clique em Gerar de novo."
)

// posaulaStatusEfetivo é o ai_status como a tela deve ver: pending/running
// com ai_updated_at mais velho que posaulaGeracaoExpiraEm vira failed com
// uma mensagem que diz o que fazer. Pura — recebe o "agora". Sem
// ai_updated_at não dá pra julgar; devolve como está.
func posaulaStatusEfetivo(status, aiErr *string, updatedAt *time.Time, now time.Time) (*string, *string) {
	if status == nil || updatedAt == nil {
		return status, aiErr
	}
	if *status != "pending" && *status != "running" {
		return status, aiErr
	}
	if now.Sub(*updatedAt) <= posaulaGeracaoExpiraEm {
		return status, aiErr
	}
	failed, msg := "failed", posaulaErroExpirou
	return &failed, &msg
}

// ── Input da resposta ────────────────────────────────────────────────────────

// posaulaAnswerTextMax: uma resposta aberta de exercício de aula; 8 mil
// caracteres já é um texto longo — acima disso é colagem, não resposta.
const posaulaAnswerTextMax = 8_000

type posaulaRespostaInput struct {
	SelectedOption *int    `json:"selectedOption,omitempty"`
	AnswerText     *string `json:"answerText,omitempty"`
}

// validate confere o que dá pra conferir ANTES de saber o tipo da prática
// (que está no banco): precisa vir pelo menos um dos dois campos e o texto
// respeita o teto. O resto é validarRespostaPorTipo.
func (in *posaulaRespostaInput) validate() error {
	if in.AnswerText != nil {
		t := strings.TrimSpace(*in.AnswerText)
		in.AnswerText = &t
		if utf8.RuneCountInString(t) > posaulaAnswerTextMax {
			return validationErr(fmt.Sprintf("a resposta deve ter no máximo %d caracteres", posaulaAnswerTextMax))
		}
	}
	if in.SelectedOption == nil && (in.AnswerText == nil || *in.AnswerText == "") {
		return validationErr("envie selectedOption (múltipla escolha) ou answerText (resposta aberta)")
	}
	return nil
}

// validarRespostaPorTipo é a checagem que depende do tipo da prática: mc
// exige uma opção dentro da faixa; aberta exige texto. Pura, testável.
func validarRespostaPorTipo(kind string, nOpcoes int, in posaulaRespostaInput) error {
	switch kind {
	case "mc":
		if in.SelectedOption == nil {
			return validationErr("esta prática é de múltipla escolha: envie selectedOption")
		}
		if *in.SelectedOption < 0 || *in.SelectedOption >= nOpcoes {
			return validationErr(fmt.Sprintf("selectedOption deve ficar entre 0 e %d", nOpcoes-1))
		}
	case "aberta":
		if in.AnswerText == nil || strings.TrimSpace(*in.AnswerText) == "" {
			return validationErr("esta prática é de resposta aberta: envie answerText")
		}
	default:
		return fmt.Errorf("prática com tipo desconhecido %q", kind)
	}
	return nil
}

// posaulaStatusFrom lê ?status= da lista do aluno: ausente = pending; só
// pending|done são válidos.
func posaulaStatusFrom(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "pending":
		return "pending", nil
	case "done":
		return "done", nil
	}
	return "", validationErr("status deve ser pending ou done")
}

const (
	posaulaMinhasPraticasLimitDefault = 50
	posaulaMinhasPraticasLimitMax     = 200
)

// posaulaLimitFrom lê ?limit= da lista do aluno: ausente = 50, fora de
// 1..200 é ajustado pro limite (como days em portalDiaryDaysFrom — filtro
// de listagem não derruba a tela), texto que não é número é 400.
func posaulaLimitFrom(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return posaulaMinhasPraticasLimitDefault, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, validationErr(fmt.Sprintf("limit inválido (use um número de 1 a %d)", posaulaMinhasPraticasLimitMax))
	}
	if n < 1 {
		n = 1
	}
	if n > posaulaMinhasPraticasLimitMax {
		n = posaulaMinhasPraticasLimitMax
	}
	return n, nil
}

// posaulaOptionalQueryID lê ?classId= (opcional): ausente = nil, presente
// tem que ser inteiro positivo.
func posaulaOptionalQueryID(r *http.Request, name string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, validationErr(name + " inválido")
	}
	return &id, nil
}

// posaulaParseOptions desserializa o JSONB de opções (nunca nil no JSON de
// saída — o front faz options.map).
func posaulaParseOptions(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

// ── Aluno ────────────────────────────────────────────────────────────────────

// posaulaPortalUserID acha o "user".id do Portal pelo e-mail da sessão do
// auth central — a mesma ponte de portalMyOverview/portalMySessions (os ids
// dos dois bancos não coincidem; ver portalUserIDByEmail). ok=false quando
// a pessoa não existe no Portal (nunca foi matriculada): não é erro, é
// lista vazia.
func (s *Server) posaulaPortalUserID(ctx context.Context, email string) (int64, bool, error) {
	id, err := s.portalUserIDByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// posaulaMinhasPraticas lista as práticas JÁ LIBERADAS (available_from <=
// now) e não excluídas do aluno, as `limit` mais recentes por data da aula
// (desc, desempate por id desc). pending = sem resposta; done = com
// resposta. A lista "done" só cresce (4 práticas por aula, pra sempre) —
// sem o teto um aluno antigo puxava centenas de cards a cada refetch.
func (s *Server) posaulaMinhasPraticas(ctx context.Context, userID int64, status string, classID *int64, limit int) ([]posaulaPraticaDTO, error) {
	args := []any{userID}
	where := `pt.user_id = $1 AND pt.deleted_at IS NULL AND pt.available_from <= now()`
	if classID != nil {
		args = append(args, *classID)
		where += fmt.Sprintf(" AND cs.class_id = $%d", len(args))
	}
	if status == "done" {
		where += " AND pa.id IS NOT NULL"
	} else {
		where += " AND pa.id IS NULL"
	}
	args = append(args, limit)
	rows, err := s.portalDB.Query(ctx, `
		SELECT pt.id::text, pt.session_id::text, to_char(cs.date,'YYYY-MM-DD'), cs.class_id::text, COALESCE(cl.name,''),
		       pt.title, pt.statement, pt.kind, pt.options::text, pt.hint, pt.difficulty, pt.available_from, pt.answer_key,
		       pa.id, pa.answer_text, pa.selected_option, pa.is_correct, pa.feedback, pa.answered_at
		FROM posaula_task pt
		JOIN class_session cs ON cs.id = pt.session_id
		JOIN class cl ON cl.id = cs.class_id
		LEFT JOIN posaula_answer pa ON pa.task_id = pt.id
		WHERE `+where+fmt.Sprintf(` ORDER BY cs.date DESC, pt.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	itens := []posaulaPraticaDTO{}
	for rows.Next() {
		var d posaulaPraticaDTO
		var options, answerKey string
		var answerID *int64
		var answerText, feedback *string
		var selected *int
		var isCorrect *bool
		var answeredAt *time.Time
		if err := rows.Scan(&d.ID, &d.SessionID, &d.SessionDate, &d.ClassID, &d.ClassName,
			&d.Title, &d.Statement, &d.Kind, &options, &d.Hint, &d.Difficulty, &d.AvailableFrom, &answerKey,
			&answerID, &answerText, &selected, &isCorrect, &feedback, &answeredAt); err != nil {
			return nil, err
		}
		d.Options = posaulaParseOptions(options)
		if d.ClassName == "" {
			d.ClassName = "Turma " + d.ClassID
		}
		if answerID != nil && answeredAt != nil {
			d.Answer = &posaulaRespostaDTO{
				AnswerText: answerText, SelectedOption: selected, IsCorrect: isCorrect, Feedback: feedback,
				AnsweredAt: *answeredAt,
			}
			// O gabarito de mc só depois de responder — e da aberta nunca
			// (é critério de correção, não "a resposta").
			if d.Kind == "mc" {
				if idx, err := strconv.Atoi(answerKey); err == nil {
					d.Answer.CorrectOption = &idx
				}
			}
		}
		itens = append(itens, d)
	}
	return itens, rows.Err()
}

// posaulaPraticaDoAluno é o que o POST /answer precisa saber antes de gravar.
type posaulaPraticaDoAluno struct {
	ID        int64
	Kind      string
	Options   []string
	AnswerKey string
	Hint      string
	Answered  bool
}

// posaulaCarregarPraticaDoAluno acha a prática SE ela é do aluno e já está
// liberada; qualquer outra coisa é 404 (não confirma que o id existe — mesma
// regra do download de arquivo do diário).
func (s *Server) posaulaCarregarPraticaDoAluno(ctx context.Context, userID, taskID int64) (*posaulaPraticaDoAluno, error) {
	var p posaulaPraticaDoAluno
	var options string
	err := s.portalDB.QueryRow(ctx, `
		SELECT pt.id, pt.kind, pt.options::text, pt.answer_key, pt.hint, EXISTS(SELECT 1 FROM posaula_answer pa WHERE pa.task_id = pt.id)
		FROM posaula_task pt
		WHERE pt.id = $1 AND pt.user_id = $2 AND pt.deleted_at IS NULL AND pt.available_from <= now()`, taskID, userID).
		Scan(&p.ID, &p.Kind, &options, &p.AnswerKey, &p.Hint, &p.Answered)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Prática")
	}
	if err != nil {
		return nil, err
	}
	p.Options = posaulaParseOptions(options)
	return &p, nil
}

// posaulaFeedbackMC monta o retorno imediato da múltipla escolha. A dica só
// entra quando errou — quem acertou não precisa dela.
func posaulaFeedbackMC(correta bool, opcaoCerta, dica string) string {
	if correta {
		return "Correto!"
	}
	msg := fmt.Sprintf("A resposta certa era “%s”.", opcaoCerta)
	if strings.TrimSpace(dica) != "" {
		msg += " Dica: " + strings.TrimSpace(dica)
	}
	return msg
}

// posaulaResponder grava a resposta. mc já sai corrigida (servidor); aberta
// entra sem veredito e a correção pelo Claude é enfileirada pelo chamador.
// 409 se já respondida (responder é definitivo). A checagem "já respondida"
// aqui é a de conforto; a de verdade é o UNIQUE em task_id (23505 → 409 via
// portalDBErr), que segura dois cliques simultâneos.
func (s *Server) posaulaResponder(ctx context.Context, userID int64, p *posaulaPraticaDoAluno, in posaulaRespostaInput) (*posaulaRespostaDTO, int64, error) {
	if p.Answered {
		return nil, 0, conflictErr("esta prática já foi respondida")
	}
	if err := validarRespostaPorTipo(p.Kind, len(p.Options), in); err != nil {
		return nil, 0, err
	}
	var (
		answerText  *string
		selected    *int
		isCorrect   *bool
		feedback    *string
		correctedAt *time.Time
	)
	if p.Kind == "mc" {
		idx := *in.SelectedOption
		selected = &idx
		certa, _ := strconv.Atoi(p.AnswerKey)
		ok := idx == certa
		isCorrect = &ok
		opcaoCerta := ""
		if certa >= 0 && certa < len(p.Options) {
			opcaoCerta = p.Options[certa]
		}
		fb := posaulaFeedbackMC(ok, opcaoCerta, p.Hint)
		feedback = &fb
		agora := time.Now()
		correctedAt = &agora
	} else {
		answerText = in.AnswerText
	}
	var out posaulaRespostaDTO
	var answerID int64
	err := s.portalDB.QueryRow(ctx, `
		INSERT INTO posaula_answer (task_id, user_id, answer_text, selected_option, is_correct, feedback, answered_at, corrected_at)
		VALUES ($1, $2, $3, $4, $5, $6, now(), $7)
		RETURNING id, answer_text, selected_option, is_correct, feedback, answered_at`,
		p.ID, userID, answerText, selected, isCorrect, feedback, correctedAt).
		Scan(&answerID, &out.AnswerText, &out.SelectedOption, &out.IsCorrect, &out.Feedback, &out.AnsweredAt)
	if err != nil {
		err = portalDBErr(err)
		var ae *AppError
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			return nil, 0, conflictErr("esta prática já foi respondida")
		}
		return nil, 0, err
	}
	if p.Kind == "mc" {
		if idx, err := strconv.Atoi(p.AnswerKey); err == nil {
			out.CorrectOption = &idx
		}
	}
	return &out, answerID, nil
}

// posaulaMarcarCorrecaoFalhou é o fallback da correção automática: o aluno
// não fica esperando um feedback que não vai chegar, e o professor vê a
// resposta na tela da aula. Só mexe em resposta ainda sem correção.
func (s *Server) posaulaMarcarCorrecaoFalhou(ctx context.Context, answerID int64, runID *int64) error {
	_, err := s.portalDB.Exec(ctx, `UPDATE posaula_answer SET feedback = $2, corrected_at = now(), ai_run_id = COALESCE($3, ai_run_id)
		WHERE id = $1 AND corrected_at IS NULL`, answerID, posaulaFeedbackFalhou, runID)
	return err
}

const posaulaFeedbackFalhou = "Não consegui corrigir automaticamente — o professor vai olhar."

// ── Professor ────────────────────────────────────────────────────────────────

// posaulaPraticasDaAula lista as práticas (não excluídas) de uma aula, com
// o aluno de cada uma e o estado da geração. Aula inexistente → 404.
func (s *Server) posaulaPraticasDaAula(ctx context.Context, sessionID int64) (*posaulaPraticasDaAulaDTO, error) {
	if err := s.portalSessionExists(ctx, sessionID); err != nil {
		return nil, err
	}
	out := &posaulaPraticasDaAulaDTO{Tasks: []posaulaPraticaProfessorDTO{}}
	var aiUpdatedAt *time.Time
	err := s.portalDB.QueryRow(ctx, `SELECT ai_status, ai_error, ai_updated_at FROM session_diary WHERE session_id = $1`, sessionID).
		Scan(&out.AiStatus, &out.AiError, &aiUpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out.AiStatus, out.AiError = posaulaStatusEfetivo(out.AiStatus, out.AiError, aiUpdatedAt, time.Now())
	rows, err := s.portalDB.Query(ctx, `
		SELECT pt.id::text, pt.user_id::text, COALESCE(u.name,''), pt.title, pt.statement, pt.kind, pt.options::text,
		       pt.answer_key, pt.hint, pt.difficulty, pt.available_from, pa.id IS NOT NULL, pa.is_correct,
		       pa.answer_text, pa.feedback, pa.corrected_at
		FROM posaula_task pt
		LEFT JOIN "user" u ON u.id = pt.user_id
		LEFT JOIN posaula_answer pa ON pa.task_id = pt.id
		WHERE pt.session_id = $1 AND pt.deleted_at IS NULL
		ORDER BY COALESCE(u.name,''), pt.user_id, pt.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d posaulaPraticaProfessorDTO
		var options string
		if err := rows.Scan(&d.ID, &d.StudentID, &d.StudentName, &d.Title, &d.Statement, &d.Kind, &options,
			&d.AnswerKey, &d.Hint, &d.Difficulty, &d.AvailableFrom, &d.Answered, &d.IsCorrect,
			&d.AnswerText, &d.Feedback, &d.CorrectedAt); err != nil {
			return nil, err
		}
		d.Options = posaulaParseOptions(options)
		if d.StudentName == "" {
			d.StudentName = "Aluno " + d.StudentID
		}
		out.Tasks = append(out.Tasks, d)
	}
	return out, rows.Err()
}

// posaulaExcluirPratica tira a prática do ar (soft delete). Devolve o
// session_id pra auditoria. Já excluída ou inexistente → 404.
func (s *Server) posaulaExcluirPratica(ctx context.Context, taskID int64) (int64, error) {
	var sessionID int64
	err := s.portalDB.QueryRow(ctx, `UPDATE posaula_task SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL RETURNING session_id`, taskID).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, notFoundErr("Prática")
	}
	return sessionID, err
}

// posaulaDiarioVersao devolve o updated_at do diário da aula — é a "versão"
// que entra na chave de idempotência da geração. Sem diário → 404 (não há
// o que gerar).
func (s *Server) posaulaDiarioVersao(ctx context.Context, sessionID int64) (time.Time, error) {
	var t time.Time
	err := s.portalDB.QueryRow(ctx, `SELECT updated_at FROM session_diary WHERE session_id = $1`, sessionID).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := s.portalSessionExists(ctx, sessionID); err != nil {
			return time.Time{}, err
		}
		return time.Time{}, notFoundErr("Diário desta aula")
	}
	return t, err
}
