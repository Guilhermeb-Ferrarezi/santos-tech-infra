package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Material vivo (Pós-aula, fase 3) — a orquestração: fila, banco e o handler
// asynq. A parte pura (briefs, parse, patch) está em posaula_material_prompt.go.
//
// Um material por CURSO (tabela course_doc), com histórico completo em
// course_doc_revision. Dois caminhos:
//   - SEMENTE (sessionId 0): o Claude (opus) escreve o material do zero a
//     partir da descrição do curso, do currículo e do conteúdo contratado.
//     Pedida pelo admin (POST …/doc/seed) ou automaticamente quando a
//     primeira aula de uma turma desse curso tem as práticas geradas;
//   - AULA (sessionId > 0): o Claude (sonnet) lê o material atual e o diário
//     da aula e devolve um PATCH por seções com o que a aula acrescenta de
//     novo; "sem novidade" não cria versão.
//
// Sem aprovação humana (decisão do Rodrigo): o que substitui a aprovação é o
// histórico com "voltar pra esta versão" (handlers_portal_material.go).

const (
	TaskPosaulaMaterial = "posaula:material"
	// materialSementeTimeout: teto de UMA chamada ao opus na semente — 12 mil
	// caracteres de saída não cabem nos 2 min do padrão (agent_client.go).
	materialSementeTimeout = 5 * time.Minute
)

// materialTaskOpts: mesma fila e retentativas das práticas, mas com 22 min
// de teto — o pior caso é o da SEMENTE: 2 tentativas de parse × 2 tentativas
// HTTP (claudeRawTentativas) × materialSementeTimeout (5 min) = 20 min, mais
// folga pra gravar. O patch de uma aula (sonnet, brief menor) fica bem
// abaixo disso; usar o mesmo teto para os dois não custa nada a ele.
var materialTaskOpts = []asynq.Option{
	asynq.MaxRetry(2),
	asynq.Timeout(22 * time.Minute),
	asynq.Queue(posaulaAsynqQueue),
}

// materialEsperaSemente: quando o patch de uma aula chega pra um curso cuja
// semente já está pendente/rodando (ou já existiu e falhou), quanto tempo
// ele espera antes da primeira tentativa — dá chance da semente terminar
// antes de checar de novo (ver materialEnfileirarAposAula e materialGerar).
const materialEsperaSemente = 90 * time.Second

// materialErroSementeEmAndamento: erro retentável que materialGerar devolve
// quando o patch de uma aula chega e o curso ainda não tem NENHUMA versão
// (a semente está em andamento, ou nem começou) — nunca vira uma segunda
// semente (custaria outra chamada ao opus e poderia sobrescrever o patch de
// outra aula); só espera e tenta de novo.
var materialErroSementeEmAndamento = errors.New("material: a semente do curso ainda não terminou")

// materialPayload é o corpo da task. Versao entra na chave de idempotência:
// na semente é o instante do pedido (cada "Regerar" gera de novo); na aula é
// o updated_at do diário (a mesma versão do diário só entra uma vez no
// material, mesmo que as práticas sejam regeradas). AposSessionID: na
// semente automática, a aula que disparou — quando a semente termina, essa
// aula é incorporada em seguida (senão a primeira aula ficaria de fora).
// AuthorEmail: quem pediu (admin no seed manual), só pra auditoria.
type materialPayload struct {
	CourseID      int64     `json:"courseId"`
	SessionID     int64     `json:"sessionId"`
	Versao        time.Time `json:"versao"`
	AposSessionID int64     `json:"aposSessionId,omitempty"`
	AuthorEmail   string    `json:"authorEmail,omitempty"`
}

// materialTaskID: fixo por (curso) na semente e por (curso, aula) no patch —
// é o que deduplica dois pedidos iguais na fila.
func materialTaskID(courseID, sessionID int64) string {
	if sessionID <= 0 {
		return fmt.Sprintf("posaula:material:%d", courseID)
	}
	return fmt.Sprintf("posaula:material:%d:s%d", courseID, sessionID)
}

// materialIdempotencyKey — sha256("material" | curso | aula | versão | versão do prompt).
func materialIdempotencyKey(courseID, sessionID int64, versao time.Time) string {
	return sha256Hex(fmt.Sprintf("material|%d|%d|%s|%s", courseID, sessionID, versao.UTC().Format(time.RFC3339Nano), materialPromptVersion))
}

// ── DTOs ─────────────────────────────────────────────────────────────────────

// courseDocDTO é o material como o STAFF vê. version 0 com bodyMd vazio =
// a linha existe só pra carregar o estado da semente (pending/failed) —
// ainda não há material pra ler.
type courseDocDTO struct {
	CourseID  string    `json:"courseId"`
	BodyMd    string    `json:"bodyMd"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
	AiStatus  *string   `json:"aiStatus"`
	AiError   *string   `json:"aiError"`
}

// courseDocRevisionDTO é uma versão do histórico. BodyMd só vem no GET de
// UMA revisão (a lista traria o material inteiro N vezes).
type courseDocRevisionDTO struct {
	Version         int       `json:"version"`
	Changelog       string    `json:"changelog"`
	Source          string    `json:"source"`
	SourceSessionID *string   `json:"sourceSessionId"`
	AuthorEmail     *string   `json:"authorEmail"`
	CreatedAt       time.Time `json:"createdAt"`
	Chars           int       `json:"chars"`
	BodyMd          string    `json:"bodyMd,omitempty"`
}

// courseDocAlunoDTO é o material como o ALUNO vê: sem estado de IA.
type courseDocAlunoDTO struct {
	CourseID   string    `json:"courseId"`
	CourseName string    `json:"courseName"`
	BodyMd     string    `json:"bodyMd"`
	Version    int       `json:"version"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// ── Enfileirar ───────────────────────────────────────────────────────────────

// enqueueMaterial marca o material como pending e enfileira a task. Com o
// TaskID já na fila: arquivada/concluída → solta e enfileira de novo;
// ATIVA (já leu o material/diário) → segunda task com id versionado
// agendada pra 30 s; pendente/agendada → nada a fazer (vai ler o estado
// atual quando rodar). Mesmo desenho de posaulaEnfileirarComConflito.
func (s *Server) enqueueMaterial(ctx context.Context, p materialPayload) error {
	return s.enqueueMaterialComOpts(ctx, p)
}

// enqueueMaterialAdiado é o enqueueMaterial com um atraso inicial
// (asynq.ProcessIn) — usado quando o patch de uma aula precisa esperar a
// semente do curso terminar (ver materialEnfileirarAposAula e
// materialEnfileirarComConflito) em vez de virar uma segunda semente. O
// TaskID continua sendo o da AULA (materialTaskID com sessionID>0), então
// nunca conflita com o TaskID da semente (sessionID 0).
func (s *Server) enqueueMaterialAdiado(ctx context.Context, p materialPayload, espera time.Duration) error {
	return s.enqueueMaterialComOpts(ctx, p, asynq.ProcessIn(espera))
}

func (s *Server) enqueueMaterialComOpts(ctx context.Context, p materialPayload, extra ...asynq.Option) error {
	if s.queue == nil {
		return errFilaIndisponivel
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := s.courseDocSetAIStatus(ctx, p.CourseID, "pending", nil); err != nil {
		return err
	}
	id := materialTaskID(p.CourseID, p.SessionID)
	opts := append(append([]asynq.Option{}, materialTaskOpts...), asynq.TaskID(id))
	opts = append(opts, extra...)
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(TaskPosaulaMaterial, b, opts...))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		err = s.materialEnfileirarComConflito(ctx, id, b, p)
	}
	if err != nil {
		msg := "não consegui enfileirar a geração do material: " + err.Error()
		_ = s.courseDocSetAIStatus(ctx, p.CourseID, "failed", &msg)
		return err
	}
	return nil
}

// materialEnfileirarComConflito decide o que fazer quando o TaskID já está
// na fila. p.AposSessionID > 0 identifica um pedido de SEMENTE que uma AULA
// disparou (ver materialEnfileirarAposAula) — nesse caso, em QUALQUER estado
// que não seja "já terminou" (Archived/Completed), a aula NUNCA vira uma
// segunda semente: agenda o PATCH dela pra depois, com TaskID próprio (não
// conflita com o da semente). Sem AposSessionID (pedido manual de
// "Gerar"/"Regerar", ou o patch normal de uma aula) o comportamento é o de
// sempre: Active agenda uma segunda geração versionada pra 30 s; Pending/
// Retry é engolido (a que já está lá vai rodar).
func (s *Server) materialEnfileirarComConflito(ctx context.Context, id string, payload []byte, p materialPayload) error {
	state, ok := s.posaulaEstadoDaTask(id)
	if !ok {
		return nil
	}
	base := append([]asynq.Option{}, materialTaskOpts...)
	switch state {
	case asynq.TaskStateArchived, asynq.TaskStateCompleted:
		if !s.posaulaSoltarTask(id, state) {
			return nil
		}
		_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(TaskPosaulaMaterial, payload, append(base, asynq.TaskID(id))...))
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return err
	case asynq.TaskStateActive:
		if p.AposSessionID > 0 {
			return s.materialAgendarPatchAposSemente(ctx, p.CourseID, p.AposSessionID)
		}
		idV := fmt.Sprintf("%s:v%d", id, p.Versao.UnixNano())
		_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(TaskPosaulaMaterial, payload, append(base, asynq.TaskID(idV), asynq.ProcessIn(posaulaReenfileirarEm))...))
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		if err == nil {
			slog.Info("material: pedido durante geração ativa — segunda geração agendada", "course", p.CourseID, "task", idV, "em", posaulaReenfileirarEm)
		}
		return err
	default:
		// Pending, Scheduled ou Retry: a semente/patch que já está lá ainda
		// não rodou. Se foi uma aula que disparou este pedido de semente,
		// ela não pode ficar de fora — agenda o patch dela em vez de ser
		// engolida junto com o pedido duplicado.
		if p.AposSessionID > 0 {
			return s.materialAgendarPatchAposSemente(ctx, p.CourseID, p.AposSessionID)
		}
		return nil
	}
}

// materialAgendarPatchAposSemente enfileira o PATCH de uma aula (TaskID
// próprio da aula — nunca o da semente) pra rodar depois de dar tempo da
// semente do curso terminar. Chamado quando a aula chegou como pedido de
// semente mas outra aula (ou o admin) já tinha uma pra este curso em
// andamento — nunca uma segunda chamada ao opus.
func (s *Server) materialAgendarPatchAposSemente(ctx context.Context, courseID, sessionID int64) error {
	var updatedAt time.Time
	err := s.portalDB.QueryRow(ctx, `SELECT updated_at FROM session_diary WHERE session_id = $1`, sessionID).Scan(&updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // o diário sumiu nesse meio-tempo (aula apagada) — nada a fazer
	}
	if err != nil {
		return err
	}
	return s.enqueueMaterialAdiado(ctx, materialPayload{CourseID: courseID, SessionID: sessionID, Versao: updatedAt}, materialEsperaSemente)
}

// materialEnfileirarAposAula é o gancho do fim de posaula:gerar: curso sem
// NENHUM course_doc → semente, que depois incorpora esta aula
// (AposSessionID); curso com course_doc mas version 0 (semente já pedida —
// pending/running/failed — por outra aula ou pelo admin) → NUNCA outra
// semente: o patch desta aula é agendado pra depois, com um TaskID só dele
// (materialAgendarPatchAposSemente); curso com material (version > 0) →
// patch desta aula, na hora. Nunca falha o chamador — só loga.
func (s *Server) materialEnfileirarAposAula(ctx context.Context, courseID, sessionID int64) {
	if courseID <= 0 || s.queue == nil {
		return
	}
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	var version int
	err := s.portalDB.QueryRow(fctx, `SELECT version FROM course_doc WHERE course_id = $1`, courseID).Scan(&version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("material: não consegui conferir se o curso tem material", "course", courseID, "err", err)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Primeira aula deste curso: dispara a semente, que incorpora esta
		// aula assim que terminar.
		if err := s.enqueueMaterial(fctx, materialPayload{CourseID: courseID, Versao: time.Now(), AposSessionID: sessionID}); err != nil {
			slog.Warn("material: não consegui enfileirar após a aula", "course", courseID, "session", sessionID, "err", err)
		}
		return
	}
	var updatedAt time.Time
	if err := s.portalDB.QueryRow(fctx, `SELECT updated_at FROM session_diary WHERE session_id = $1`, sessionID).Scan(&updatedAt); err != nil {
		slog.Warn("material: não consegui ler a versão do diário", "session", sessionID, "err", err)
		return
	}
	p := materialPayload{CourseID: courseID, SessionID: sessionID, Versao: updatedAt}
	if version == 0 {
		// Já existe (ou já existiu) um pedido de semente pra este curso —
		// não cria outro; o patch espera ela terminar antes de rodar.
		if err := s.enqueueMaterialAdiado(fctx, p, materialEsperaSemente); err != nil {
			slog.Warn("material: não consegui agendar o patch após a semente", "course", courseID, "session", sessionID, "err", err)
		}
		return
	}
	if err := s.enqueueMaterial(fctx, p); err != nil {
		slog.Warn("material: não consegui enfileirar após a aula", "course", courseID, "session", sessionID, "err", err)
	}
}

// ── Store ────────────────────────────────────────────────────────────────────

// courseDocSetAIStatus grava o estado da geração — cria a linha (version 0,
// sem material) se for a primeira vez. A FK pra course (quando existe) é o
// que impede criar estado pra curso inexistente; o handler confere antes.
func (s *Server) courseDocSetAIStatus(ctx context.Context, courseID int64, status string, errMsg *string) error {
	_, err := s.portalDB.Exec(ctx, `INSERT INTO course_doc (course_id, ai_status, ai_error, ai_updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (course_id) DO UPDATE SET ai_status = EXCLUDED.ai_status, ai_error = EXCLUDED.ai_error, ai_updated_at = now()`,
		courseID, status, errMsg)
	return err
}

// courseDocGet devolve o material do curso, ou nil (sem erro) quando nunca
// foi pedido. Não confere se o curso existe — o handler faz isso.
func (s *Server) courseDocGet(ctx context.Context, courseID int64) (*courseDocDTO, error) {
	var d courseDocDTO
	var aiUpdatedAt *time.Time
	err := s.portalDB.QueryRow(ctx, `SELECT course_id::text, body_md, version, updated_at, ai_status, ai_error, ai_updated_at
		FROM course_doc WHERE course_id = $1`, courseID).
		Scan(&d.CourseID, &d.BodyMd, &d.Version, &d.UpdatedAt, &d.AiStatus, &d.AiError, &aiUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.AiStatus, d.AiError = posaulaStatusEfetivo(d.AiStatus, d.AiError, aiUpdatedAt, time.Now())
	return &d, nil
}

// courseDocVersao é o que a gravação de uma versão nova precisa saber.
type courseDocVersao struct {
	Body            string
	Source          string // seed | aula | manual | restore
	Changelog       string
	SourceSessionID *int64
	RunID           *int64
	AuthorEmail     *string
	// AiOK: marca ai_status=ok junto (caminhos do Claude). Manual/restore
	// não mexem no estado da IA.
	AiOK bool
	// EsperaVersao > 0: só grava se a versão atual for esta (o patch da aula
	// foi calculado em cima dela); 0 = grava por cima do que estiver lá.
	EsperaVersao int
}

// errMaterialVersaoMudou: alguém salvou outra versão entre a leitura e a
// gravação — o chamador relê e aplica o patch de novo.
var errMaterialVersaoMudou = errors.New("o material mudou de versão durante a geração")

// courseDocSalvarVersao grava a versão nova (version+1 no course_doc e a
// linha no histórico). O ON CONFLICT trava a linha do curso, então dois
// gravadores simultâneos nunca disputam o mesmo número. tx nil = sem
// transação externa.
func (s *Server) courseDocSalvarVersao(ctx context.Context, tx pgx.Tx, courseID int64, v courseDocVersao) (int, error) {
	set := "body_md = EXCLUDED.body_md, version = course_doc.version + 1, updated_at = now()"
	if v.AiOK {
		set += ", ai_status = 'ok', ai_error = NULL, ai_updated_at = now()"
	}
	where := ""
	if v.EsperaVersao > 0 {
		where = fmt.Sprintf(" WHERE course_doc.version = %d", v.EsperaVersao)
	}
	q := `INSERT INTO course_doc (course_id, body_md, version, updated_at, ai_status, ai_updated_at)
		VALUES ($1, $2, 1, now(), CASE WHEN $3 THEN 'ok' END, CASE WHEN $3 THEN now() END)
		ON CONFLICT (course_id) DO UPDATE SET ` + set + where + ` RETURNING version`
	var version int
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, q, courseID, v.Body, v.AiOK).Scan(&version)
	} else {
		err = s.portalDB.QueryRow(ctx, q, courseID, v.Body, v.AiOK).Scan(&version)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errMaterialVersaoMudou
	}
	if err != nil {
		return 0, portalDBErr(err)
	}
	ins := `INSERT INTO course_doc_revision (course_id, version, body_md, changelog, source, source_session_id, ai_run_id, author_email, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())`
	args := []any{courseID, version, v.Body, v.Changelog, v.Source, v.SourceSessionID, v.RunID, v.AuthorEmail}
	if tx != nil {
		_, err = tx.Exec(ctx, ins, args...)
	} else {
		_, err = s.portalDB.Exec(ctx, ins, args...)
	}
	if err != nil {
		return 0, portalDBErr(err)
	}
	return version, nil
}

// courseDocSalvarManual é o PUT do admin: versão nova com o texto dele.
// esperaVersao > 0 é concorrência otimista (a tela leu essa versão antes de
// editar): mudou por baixo (patch de uma aula, outro admin) → devolve
// errMaterialVersaoMudou (o handler vira 409, pra tela recarregar em vez de
// sobrescrever em silêncio). 0 = sem checagem (compat com quem não manda a
// versão).
func (s *Server) courseDocSalvarManual(ctx context.Context, courseID int64, body, authorEmail string, esperaVersao int) (int, error) {
	email := strings.ToLower(strings.TrimSpace(authorEmail))
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	version, err := s.courseDocSalvarVersao(ctx, tx, courseID, courseDocVersao{
		Body: body, Source: "manual", Changelog: "Edição manual", AuthorEmail: &email, EsperaVersao: esperaVersao,
	})
	if err != nil {
		return 0, err
	}
	return version, tx.Commit(ctx)
}

// courseDocRevisions lista o histórico, mais recente primeiro, sem o corpo.
func (s *Server) courseDocRevisions(ctx context.Context, courseID int64) ([]courseDocRevisionDTO, error) {
	rows, err := s.portalDB.Query(ctx, `SELECT version, changelog, source, source_session_id::text, author_email, created_at, length(body_md)
		FROM course_doc_revision WHERE course_id = $1 ORDER BY version DESC`, courseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	itens := []courseDocRevisionDTO{}
	for rows.Next() {
		var r courseDocRevisionDTO
		if err := rows.Scan(&r.Version, &r.Changelog, &r.Source, &r.SourceSessionID, &r.AuthorEmail, &r.CreatedAt, &r.Chars); err != nil {
			return nil, err
		}
		itens = append(itens, r)
	}
	return itens, rows.Err()
}

// courseDocRevision devolve UMA versão com o corpo. 404 se não existe.
func (s *Server) courseDocRevision(ctx context.Context, courseID int64, version int64) (*courseDocRevisionDTO, error) {
	var r courseDocRevisionDTO
	err := s.portalDB.QueryRow(ctx, `SELECT version, changelog, source, source_session_id::text, author_email, created_at, length(body_md), body_md
		FROM course_doc_revision WHERE course_id = $1 AND version = $2`, courseID, version).
		Scan(&r.Version, &r.Changelog, &r.Source, &r.SourceSessionID, &r.AuthorEmail, &r.CreatedAt, &r.Chars, &r.BodyMd)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Versão")
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// courseDocRestaurar cria uma versão NOVA com o corpo de uma versão antiga
// (source 'restore') — nunca apaga o que veio depois; o histórico é linear.
func (s *Server) courseDocRestaurar(ctx context.Context, courseID, version int64, authorEmail string) (int, error) {
	rev, err := s.courseDocRevision(ctx, courseID, version)
	if err != nil {
		return 0, err
	}
	email := strings.ToLower(strings.TrimSpace(authorEmail))
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	nova, err := s.courseDocSalvarVersao(ctx, tx, courseID, courseDocVersao{
		Body: rev.BodyMd, Source: "restore", Changelog: fmt.Sprintf("Voltou pra versão %d", rev.Version), AuthorEmail: &email,
	})
	if err != nil {
		return 0, err
	}
	return nova, tx.Commit(ctx)
}

// courseDocDoAluno é o material como o ALUNO lê: só se ele (casado por
// e-mail, como portalMyOverview) está matriculado em alguma turma do curso e
// o curso já tem material (version > 0). Fora disso é 404 — não confirma
// que o curso existe.
func (s *Server) courseDocDoAluno(ctx context.Context, email string, courseID int64) (*courseDocAlunoDTO, error) {
	var d courseDocAlunoDTO
	err := s.portalDB.QueryRow(ctx, `
		SELECT c.id::text, COALESCE(c.name,''), cd.body_md, cd.version, cd.updated_at
		FROM course c
		JOIN course_doc cd ON cd.course_id = c.id AND cd.version > 0
		WHERE c.id = $2 AND EXISTS (
		  SELECT 1 FROM enrollment e
		  JOIN "user" u ON u.id = e.user_id AND u.email = $1
		  JOIN class cl ON cl.id = e.class_id
		  WHERE cl.course_id = c.id)`, strings.ToLower(strings.TrimSpace(email)), courseID).
		Scan(&d.CourseID, &d.CourseName, &d.BodyMd, &d.Version, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Material")
	}
	if err != nil {
		return nil, err
	}
	if d.CourseName == "" {
		d.CourseName = "Curso " + d.CourseID
	}
	return &d, nil
}

// materialCarregarCurso lê o que a semente precisa: curso, currículo
// (módulos e fases, só os títulos) e os conteúdos contratados distintos das
// matrículas em turmas do curso. existe=false quando o curso sumiu.
func (s *Server) materialCarregarCurso(ctx context.Context, courseID int64) (*briefSementeInput, bool, error) {
	in := &briefSementeInput{}
	var descricao, nivel *string
	err := s.portalDB.QueryRow(ctx, `SELECT COALESCE(name,''), description, level_difficulty FROM course WHERE id = $1`, courseID).
		Scan(&in.Curso, &descricao, &nivel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if descricao != nil {
		in.Descricao = *descricao
	}
	if nivel != nil {
		in.Nivel = *nivel
	}
	if strings.TrimSpace(in.Curso) == "" {
		in.Curso = fmt.Sprintf("Curso %d", courseID)
	}

	rows, err := s.portalDB.Query(ctx, `SELECT m.id, COALESCE(m.name,''), COALESCE(p.name,'')
		FROM module m LEFT JOIN phase p ON p.module_id = m.id
		WHERE m.course_id = $1
		ORDER BY m.index_order, m.id, p.index_order, p.id`, courseID)
	if err != nil {
		return nil, false, err
	}
	var ultimoID int64 = -1
	for rows.Next() {
		var id int64
		var nome, fase string
		if err := rows.Scan(&id, &nome, &fase); err != nil {
			rows.Close()
			return nil, false, err
		}
		if id != ultimoID {
			in.Modulos = append(in.Modulos, briefModulo{Nome: nome})
			ultimoID = id
		}
		if strings.TrimSpace(fase) != "" {
			m := &in.Modulos[len(in.Modulos)-1]
			m.Fases = append(m.Fases, fase)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	cts, err := s.portalDB.Query(ctx, `SELECT DISTINCT e.contracted_content
		FROM enrollment e JOIN class cl ON cl.id = e.class_id
		WHERE cl.course_id = $1 AND COALESCE(e.contracted_content,'') <> ''
		ORDER BY e.contracted_content`, courseID)
	if err != nil {
		return nil, false, err
	}
	for cts.Next() {
		var c string
		if err := cts.Scan(&c); err != nil {
			cts.Close()
			return nil, false, err
		}
		in.Conteudos = append(in.Conteudos, c)
	}
	cts.Close()
	return in, true, cts.Err()
}

// materialDiario é o que o patch precisa do diário da aula.
type materialDiario struct {
	Data           string
	Summary        string
	StudentSummary string
	Attachments    []portalDiaryAttachmentDTO
	AuthorEmail    string
}

// materialCarregarDiario lê o diário; ok=false quando a aula não tem diário
// (ou não existe mais).
func (s *Server) materialCarregarDiario(ctx context.Context, sessionID int64) (*materialDiario, bool, error) {
	d := &materialDiario{}
	var attachments string
	var studentSummary *string
	err := s.portalDB.QueryRow(ctx, `SELECT to_char(cs.date,'YYYY-MM-DD'), sd.summary, sd.student_summary, sd.attachments::text, sd.author_email
		FROM session_diary sd JOIN class_session cs ON cs.id = sd.session_id
		WHERE sd.session_id = $1`, sessionID).
		Scan(&d.Data, &d.Summary, &studentSummary, &attachments, &d.AuthorEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if studentSummary != nil {
		d.StudentSummary = *studentSummary
	}
	d.Attachments = portalDiaryParseAttachments(attachments)
	return d, true, nil
}

// materialCarregarAlunosDaTurma lista os nomes dos alunos matriculados na
// turma da aula — usado só pelo cinto de segurança de privacidade do patch
// (materialPatchComNome): nenhum desses nomes pode aparecer no material do
// curso, que qualquer aluno de qualquer turma dele vai ler.
func (s *Server) materialCarregarAlunosDaTurma(ctx context.Context, sessionID int64) ([]string, error) {
	rows, err := s.portalDB.Query(ctx, `
		SELECT COALESCE(u.name,'')
		FROM class_session cs
		JOIN enrollment e ON e.class_id = cs.class_id
		JOIN "user" u ON u.id = e.user_id
		WHERE cs.id = $1`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nomes []string
	for rows.Next() {
		var nome string
		if err := rows.Scan(&nome); err != nil {
			return nil, err
		}
		if strings.TrimSpace(nome) != "" {
			nomes = append(nomes, nome)
		}
	}
	return nomes, rows.Err()
}

// ── Handler asynq ────────────────────────────────────────────────────────────

func (s *Server) handlePosaulaMaterial(ctx context.Context, t *asynq.Task) error {
	var p materialPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil || p.CourseID <= 0 {
		slog.Error("material: task com payload inválido", "err", err)
		return asynq.SkipRetry
	}
	return s.materialGerar(ctx, p)
}

// materialGerar é o miolo do handler: decide entre semente e patch.
//
// p.SessionID <= 0 é sempre um pedido de SEMENTE (seed manual, "Regerar", ou
// automático — a 1ª aula de um curso sem course_doc NENHUM, ver
// materialEnfileirarAposAula). p.SessionID > 0 é sempre o PATCH de uma aula:
// se o curso ainda não tem NENHUMA versão (doc nil ou version 0) — a semente
// está pending/running, ou nem começou —, o patch NUNCA vira uma segunda
// semente; só espera e tenta de novo (materialAguardarSemente). Isso não
// deveria acontecer na prática (materialEnfileirarAposAula só agenda o patch
// depois de conferir o estado), mas fica como cinto de segurança pra uma
// corrida rara entre duas aulas do mesmo curso.
func (s *Server) materialGerar(ctx context.Context, p materialPayload) error {
	inicio := time.Now()
	key := materialIdempotencyKey(p.CourseID, p.SessionID, p.Versao)
	if ok, err := s.posaulaRunOKExiste(ctx, key); err != nil {
		return fmt.Errorf("material: conferir idempotência: %w", err)
	} else if ok {
		return s.materialConfirmarOK(ctx, p.CourseID)
	}
	curso, existe, err := s.materialCarregarCurso(ctx, p.CourseID)
	if err != nil {
		msg := "erro ao ler o curso: " + err.Error()
		s.materialSetAIStatusFinal(ctx, p.CourseID, "failed", &msg)
		return fmt.Errorf("material: carregar curso: %w", err)
	}
	if !existe {
		slog.Warn("material: curso não existe mais, geração descartada", "course", p.CourseID)
		return nil
	}
	if p.SessionID > 0 {
		doc, err := s.courseDocGet(ctx, p.CourseID)
		if err != nil {
			msg := "erro ao ler o material: " + err.Error()
			s.materialSetAIStatusFinal(ctx, p.CourseID, "failed", &msg)
			return fmt.Errorf("material: carregar material: %w", err)
		}
		if doc == nil || doc.Version == 0 {
			return s.materialAguardarSemente(ctx, p)
		}
		if err := s.courseDocSetAIStatus(ctx, p.CourseID, "running", nil); err != nil {
			return fmt.Errorf("material: marcar running: %w", err)
		}
		return s.materialIncorporarAula(ctx, p, key, curso.Curso, doc, inicio)
	}
	if err := s.courseDocSetAIStatus(ctx, p.CourseID, "running", nil); err != nil {
		return fmt.Errorf("material: marcar running: %w", err)
	}
	return s.materialSemear(ctx, p, key, curso, inicio)
}

// materialAguardarSemente: o patch de uma aula chegou pra rodar (via
// materialEnfileirarAposAula/materialAgendarPatchAposSemente) mas o curso
// ainda não tem NENHUMA versão gravada — a semente está em andamento, ou nem
// começou. NUNCA vira uma segunda semente (custaria outra chamada ao opus e
// poderia sobrescrever o patch de outra aula): com retentativa sobrando,
// devolve um erro retentável pro asynq tentar de novo mais tarde; na última,
// desiste em silêncio — o course_doc já reflete o que aconteceu com a
// semente (ok, failed, ou ainda rodando) e esta aula só entra no material na
// próxima vez que o diário dela for salvo de novo.
func (s *Server) materialAguardarSemente(ctx context.Context, p materialPayload) error {
	if !posaulaUltimaTentativa(ctx) {
		slog.Info("material: patch da aula esperando a semente do curso terminar, o asynq tenta de novo", "course", p.CourseID, "session", p.SessionID)
		return materialErroSementeEmAndamento
	}
	slog.Warn("material: semente do curso não terminou a tempo, patch da aula descartado", "course", p.CourseID, "session", p.SessionID)
	return nil
}

// materialSemear escreve o material do zero (opus). Resposta inválida ganha
// UMA segunda chance com o motivo no brief.
func (s *Server) materialSemear(ctx context.Context, p materialPayload, key string, in *briefSementeInput, inicio time.Time) error {
	brief := montarBriefSemente(*in)
	raw, err := s.claudeRawCom(ctx, brief, materialModeloSemente, materialSementeTimeout)
	if err != nil {
		return s.materialFalharChamada(ctx, p, brief, err, time.Since(inicio))
	}
	body, perr := parseMaterialSemente(raw)
	if perr != nil {
		slog.Warn("material: semente inválida, pedindo de novo", "course", p.CourseID, "err", perr)
		in.ErroAnterior = perr.Error()
		brief = montarBriefSemente(*in)
		raw, err = s.claudeRawCom(ctx, brief, materialModeloSemente, materialSementeTimeout)
		if err != nil {
			return s.materialFalharChamada(ctx, p, brief, err, time.Since(inicio))
		}
		if body, perr = parseMaterialSemente(raw); perr != nil {
			return s.materialFalhar(ctx, p, brief, raw, "resposta do Claude inválida: "+perr.Error(), time.Since(inicio))
		}
	}
	version, err := s.materialGravar(ctx, p, key, courseDocVersao{
		Body: body, Source: "seed", Changelog: "Material inicial escrito pelo Claude", AiOK: true,
	}, materialModeloSemente, brief, raw, time.Since(inicio))
	if err != nil {
		return err
	}
	if version > 0 {
		s.posaulaLogActivityBackground(ctx, p.AuthorEmail, "material_semeado", "course", fmt.Sprint(p.CourseID), map[string]any{
			"version": version, "chars": utf8.RuneCountInString(body),
		})
		slog.Info("material: semente gravada", "course", p.CourseID, "version", version, "chars", utf8.RuneCountInString(body), "ms", time.Since(inicio).Milliseconds())
	}
	// A aula que disparou a semente automática entra em seguida.
	if p.AposSessionID > 0 {
		s.materialEnfileirarAposAula(ctx, p.CourseID, p.AposSessionID)
	}
	return nil
}

// materialIncorporarAula pede o patch da aula (sonnet) e aplica. Se o
// material mudou de versão enquanto o patch era calculado (edição manual
// no meio), relê e aplica de novo — o patch é por seção, então reaplicar
// é seguro. doc já vem carregado do chamador (materialGerar), que também já
// confirmou que ele tem versão (version > 0).
func (s *Server) materialIncorporarAula(ctx context.Context, p materialPayload, key, curso string, doc *courseDocDTO, inicio time.Time) error {
	diario, ok, err := s.materialCarregarDiario(ctx, p.SessionID)
	if err != nil {
		msg := "erro ao ler o diário: " + err.Error()
		s.materialSetAIStatusFinal(ctx, p.CourseID, "failed", &msg)
		return fmt.Errorf("material: carregar diário: %w", err)
	}
	if !ok {
		return s.materialFalhar(ctx, p, "", "", "a aula não tem diário registrado", time.Since(inicio))
	}
	// Nomes da turma desta aula: cinto de segurança pra nenhum deles acabar
	// citado no material do CURSO (ver montarBriefPatch e
	// parsePatchMaterialSeguro). Falhar essa consulta não pode travar a
	// geração — só degrada sem a checagem extra.
	alunosDaTurma, aerr := s.materialCarregarAlunosDaTurma(ctx, p.SessionID)
	if aerr != nil {
		slog.Warn("material: não consegui listar os alunos da turma pra checar privacidade do patch", "session", p.SessionID, "err", aerr)
	}
	in := briefPatchInput{
		Curso:    curso,
		Material: materialParaBrief(doc.BodyMd, diario.Summary+" "+diario.StudentSummary, materialBriefAtualMax),
		Diario:   briefDiario{Data: diario.Data, Resumo: diario.Summary},
		Anexos:   s.posaulaLerAnexosTextoAte(ctx, diario.Attachments, materialAnexosMaxChars),
	}
	for _, a := range diario.Attachments {
		in.Diario.Anexos = append(in.Diario.Anexos, a.Name)
	}
	brief := montarBriefPatch(in)
	raw, err := s.claudeRaw(ctx, brief, materialModeloAula)
	if err != nil {
		return s.materialFalharChamada(ctx, p, brief, err, time.Since(inicio))
	}
	out, perr := parsePatchMaterialSeguro(raw, alunosDaTurma)
	if perr != nil {
		slog.Warn("material: patch inválido, pedindo de novo", "course", p.CourseID, "session", p.SessionID, "err", perr)
		in.ErroAnterior = perr.Error()
		brief = montarBriefPatch(in)
		raw, err = s.claudeRaw(ctx, brief, materialModeloAula)
		if err != nil {
			return s.materialFalharChamada(ctx, p, brief, err, time.Since(inicio))
		}
		if out, perr = parsePatchMaterialSeguro(raw, alunosDaTurma); perr != nil {
			return s.materialFalhar(ctx, p, brief, raw, "resposta do Claude inválida: "+perr.Error(), time.Since(inicio))
		}
	}
	if len(out.Secoes) == 0 {
		return s.materialSemNovidade(ctx, p, key, brief, raw, time.Since(inicio))
	}

	sid := p.SessionID
	for tentativa := 1; ; tentativa++ {
		novo, mudou := aplicarPatchMaterial(doc.BodyMd, out.Secoes)
		if !mudou {
			return s.materialSemNovidade(ctx, p, key, brief, raw, time.Since(inicio))
		}
		novo = atualizarSumario(novo)
		if n := utf8.RuneCountInString(novo); n > materialBodyMax {
			return s.materialFalhar(ctx, p, brief, raw, fmt.Sprintf("o material ficaria com %d caracteres (máximo %d)", n, materialBodyMax), time.Since(inicio))
		}
		if materialEncolheuDemais(doc.BodyMd, novo) {
			return s.materialFalhar(ctx, p, brief, raw, fmt.Sprintf("o patch reduziria o material de %d pra %d caracteres (menos de %.0f%% do tamanho atual) — nada foi gravado", utf8.RuneCountInString(doc.BodyMd), utf8.RuneCountInString(novo), materialEncolhimentoMinimo*100), time.Since(inicio))
		}
		version, err := s.materialGravar(ctx, p, key, courseDocVersao{
			Body: novo, Source: "aula", Changelog: out.Changelog, SourceSessionID: &sid, AiOK: true, EsperaVersao: doc.Version,
		}, materialModeloAula, brief, raw, time.Since(inicio))
		if errors.Is(err, errMaterialVersaoMudou) && tentativa < 3 {
			if doc, err = s.courseDocGet(ctx, p.CourseID); err != nil || doc == nil {
				msg := "erro ao reler o material"
				s.materialSetAIStatusFinal(ctx, p.CourseID, "failed", &msg)
				return fmt.Errorf("material: reler material: %v", err)
			}
			continue
		}
		if err != nil {
			return err
		}
		if version > 0 {
			s.posaulaLogActivityBackground(ctx, diario.AuthorEmail, "material_atualizado", "course", fmt.Sprint(p.CourseID), map[string]any{
				"version": version, "sessionId": fmt.Sprint(p.SessionID), "secoes": len(out.Secoes), "changelog": out.Changelog,
			})
			s.materialNotificarAdmins(ctx, p.CourseID, curso, out.Changelog)
			slog.Info("material: aula incorporada", "course", p.CourseID, "session", p.SessionID, "version", version, "secoes", len(out.Secoes), "ms", time.Since(inicio).Milliseconds())
		}
		return nil
	}
}

// materialNotificarAdmins avisa todos os administradores quando uma AULA
// muda o material do curso: o patch roda sem aprovação humana (decisão do
// Rodrigo), então o aviso — mais o histórico com "voltar pra esta versão" —
// é o que dá chance de alguém notar uma mudança ruim. Mesmo padrão do
// worker de expiração (listAdminIDs + notifyUser); falha aqui só loga, não
// desfaz a versão já gravada.
func (s *Server) materialNotificarAdmins(ctx context.Context, courseID int64, cursoNome, changelog string) {
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	admins, err := s.listAdminIDs(fctx)
	if err != nil {
		slog.Warn("material: não consegui listar os admins pra avisar da atualização", "course", courseID, "err", err)
		return
	}
	nome := vazioOu(strings.TrimSpace(cursoNome), fmt.Sprintf("Curso %d", courseID))
	titulo := fmt.Sprintf("Material do curso %s foi atualizado", nome)
	corpo := vazioOu(strings.TrimSpace(changelog), "Uma aula registrada atualizou o material do curso.")
	url := fmt.Sprintf("/dashboard/admin/portal/cursos/%d", courseID)
	for _, id := range admins {
		s.notifyUser(fctx, int32(id), titulo, corpo, url)
	}
}

// materialGravar grava numa transação o ai_run ok (com a chave) e a versão
// nova. Devolve 0 (sem erro) quando outra entrega da mesma task gravou
// primeiro (23505 na chave) ou quando a versão mudou por baixo (o chamador
// relê). Qualquer outro erro marca failed.
func (s *Server) materialGravar(ctx context.Context, p materialPayload, key string, v courseDocVersao, modelo, brief, raw string, dur time.Duration) (int, error) {
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		msg := "erro ao gravar o material: " + err.Error()
		s.materialSetAIStatusFinal(ctx, p.CourseID, "failed", &msg)
		return 0, fmt.Errorf("material: begin: %w", err)
	}
	defer tx.Rollback(ctx)
	runID, err := s.posaulaInsertRun(ctx, tx, posaulaRun{
		Key: &key, Kind: "material", SessionID: v.SourceSessionID, InputChars: utf8.RuneCountInString(brief), OutputRaw: raw, Status: "ok", Duration: dur,
		Model: modelo, PromptVersion: materialPromptVersion,
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return 0, s.materialConfirmarOK(ctx, p.CourseID)
		}
		msg := "erro ao gravar o ai_run: " + err.Error()
		s.materialSetAIStatusFinal(ctx, p.CourseID, "failed", &msg)
		return 0, fmt.Errorf("material: ai_run: %w", err)
	}
	v.RunID = &runID
	version, err := s.courseDocSalvarVersao(ctx, tx, p.CourseID, v)
	if errors.Is(err, errMaterialVersaoMudou) {
		return 0, err
	}
	if err != nil {
		msg := "erro ao gravar a versão: " + err.Error()
		s.materialSetAIStatusFinal(ctx, p.CourseID, "failed", &msg)
		return 0, fmt.Errorf("material: gravar versão: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		msg := "erro ao gravar o material: " + err.Error()
		s.materialSetAIStatusFinal(ctx, p.CourseID, "failed", &msg)
		return 0, fmt.Errorf("material: commit: %w", err)
	}
	return version, nil
}

// materialSemNovidade: a aula não acrescenta nada — registra o ai_run ok
// (com a chave, pra task reentregue não perguntar de novo) e volta o estado
// pra ok sem criar versão.
func (s *Server) materialSemNovidade(ctx context.Context, p materialPayload, key, brief, raw string, dur time.Duration) error {
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	sid := p.SessionID
	if _, err := s.posaulaInsertRun(fctx, nil, posaulaRun{
		Key: &key, Kind: "material", SessionID: &sid, InputChars: utf8.RuneCountInString(brief), OutputRaw: raw, Status: "ok", Duration: dur,
		Model: materialModeloAula, PromptVersion: materialPromptVersion,
	}); err != nil {
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23505" {
			slog.Warn("material: não consegui gravar o ai_run de 'sem novidade'", "course", p.CourseID, "session", p.SessionID, "err", err)
		}
	}
	slog.Info("material: aula sem novidade pro material", "course", p.CourseID, "session", p.SessionID)
	return s.materialConfirmarOK(ctx, p.CourseID)
}

// materialConfirmarOK: a geração já está gravada (por esta ou por outra
// entrega) — garante que o material não ficou em pending/running.
func (s *Server) materialConfirmarOK(ctx context.Context, courseID int64) error {
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	_, err := s.portalDB.Exec(fctx, `UPDATE course_doc SET ai_status = 'ok', ai_error = NULL, ai_updated_at = now()
		WHERE course_id = $1 AND ai_status IN ('pending','running')`, courseID)
	return err
}

// materialSetAIStatusFinal grava o estado final com o ctx final (o do asynq
// pode já ter sido cancelado); falha só loga.
func (s *Server) materialSetAIStatusFinal(ctx context.Context, courseID int64, status string, errMsg *string) {
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	if err := s.courseDocSetAIStatus(fctx, courseID, status, errMsg); err != nil {
		slog.Error("material: não consegui gravar o estado final", "course", courseID, "status", status, "err", err)
	}
}

// materialFalharChamada: erro transitório do agent-go com retentativa
// sobrando → ai_run failed, pending + "Tentando de novo…" e devolve o erro
// pro asynq retentar; senão falha de vez. Espelho de posaulaFalharChamada.
func (s *Server) materialFalharChamada(ctx context.Context, p materialPayload, brief string, err error, dur time.Duration) error {
	var transitorio agentTransientError
	if errors.As(err, &transitorio) && !posaulaUltimaTentativa(ctx) {
		fctx, cancel := posaulaCtxFinal(ctx)
		defer cancel()
		msg := err.Error()
		if _, ierr := s.posaulaInsertRun(fctx, nil, posaulaRun{
			Kind: "material", SessionID: materialSessionPtr(p.SessionID), InputChars: utf8.RuneCountInString(brief), Status: "failed", Error: &msg, Duration: dur,
			Model: materialModelo(p), PromptVersion: materialPromptVersion,
		}); ierr != nil {
			slog.Warn("material: não consegui gravar o ai_run da tentativa", "course", p.CourseID, "err", ierr)
		}
		tentando := posaulaErroTentandoDeNovo
		if serr := s.courseDocSetAIStatus(fctx, p.CourseID, "pending", &tentando); serr != nil {
			slog.Warn("material: não consegui marcar a retentativa", "course", p.CourseID, "err", serr)
		}
		slog.Warn("material: chamada ao Claude falhou, o asynq vai retentar", "course", p.CourseID, "err", msg)
		return err
	}
	if ferr := s.materialFalhar(ctx, p, brief, "", err.Error(), dur); ferr != nil {
		return ferr
	}
	if errors.As(err, &transitorio) {
		return err
	}
	return nil
}

// materialFalhar registra a falha DEFINITIVA: ai_run failed (sem chave) +
// material failed com a mensagem; o material anterior fica intacto.
func (s *Server) materialFalhar(ctx context.Context, p materialPayload, brief, raw, msg string, dur time.Duration) error {
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	if _, err := s.posaulaInsertRun(fctx, nil, posaulaRun{
		Kind: "material", SessionID: materialSessionPtr(p.SessionID), InputChars: utf8.RuneCountInString(brief), OutputRaw: raw, Status: "failed", Error: &msg, Duration: dur,
		Model: materialModelo(p), PromptVersion: materialPromptVersion,
	}); err != nil {
		slog.Warn("material: não consegui gravar o ai_run da falha", "course", p.CourseID, "err", err)
	}
	if err := s.courseDocSetAIStatus(fctx, p.CourseID, "failed", &msg); err != nil {
		return fmt.Errorf("material: marcar failed: %w", err)
	}
	slog.Warn("material: geração falhou", "course", p.CourseID, "session", p.SessionID, "err", msg)
	return nil
}

func materialSessionPtr(sessionID int64) *int64 {
	if sessionID <= 0 {
		return nil
	}
	return &sessionID
}

func materialModelo(p materialPayload) string {
	if p.SessionID <= 0 {
		return materialModeloSemente
	}
	return materialModeloAula
}
