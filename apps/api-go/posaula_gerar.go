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

// Pós-aula — a geração das práticas e a correção da resposta aberta, como
// tasks do asynq (mesmo Redis e mesmo asynq.Server dos emails/push, mas numa
// fila própria "posaula" — ver queue.go). O fluxo: PUT do diário (ou "gerar
// de novo") → enfileira posaula:gerar → o handler monta o brief, chama o
// Claude, valida o JSON e grava tudo numa transação → a prática fica com
// available_from = 07:00 do dia seguinte e o worker de notificação
// (posaula_worker.go) avisa o aluno quando chega a hora. Nada passa por
// aprovação (decisão do Rodrigo, docs/HISTORICO.md §8).

const (
	TaskPosaulaGerar    = "posaula:gerar"
	TaskPosaulaCorrigir = "posaula:corrigir"
	// posaulaModelo: sonnet é o suficiente pra 4 exercícios de aula e custa
	// uma fração do opus — a geração roda pra toda aula registrada.
	posaulaModelo = "sonnet"
	// posaulaAsynqQueue: fila própria do Pós-aula. Uma geração segura um
	// worker por minutos; na fila "default" ela ficaria na frente do código
	// de MFA e do reset de senha (queue.go dá 2 dos 7 workers a esta fila).
	posaulaAsynqQueue = "posaula"
	// posaulaPraticasAnterioresMax: quantas práticas passadas do aluno entram
	// no brief — só o título e acertou/errou; mais que isso é ruído.
	posaulaPraticasAnterioresMax = 30
	// posaulaReenfileirarEm: quando o diário é salvo de novo com uma geração
	// ATIVA, a segunda geração entra agendada pra daqui a isto — tempo pra a
	// ativa terminar e o professor parar de editar.
	posaulaReenfileirarEm = 30 * time.Second
	// posaulaEstadoFinalTimeout: prazo próprio pra gravar o estado final
	// (failed/ok/fallback) quando o ctx do asynq já pode ter sido cancelado.
	posaulaEstadoFinalTimeout = 15 * time.Second
	// posaulaErroTentandoDeNovo vai em ai_error enquanto o asynq ainda vai
	// retentar — o professor não vê "falhou" durante o backoff.
	posaulaErroTentandoDeNovo = "Tentando de novo…"
)

// posaulaTaskOpts: 2 retentativas (pra erro transitório de rede/banco/429),
// fila própria e 10 min de teto — cabem 2 chamadas ao Claude (a segunda é a
// retentativa de JSON inválido) × 2 tentativas HTTP × 2 min, mais as pausas
// e a gravação, sem o asynq cancelar o ctx no meio.
var posaulaTaskOpts = []asynq.Option{
	asynq.MaxRetry(2),
	asynq.Timeout(10 * time.Minute),
	asynq.Queue(posaulaAsynqQueue),
}

type posaulaGerarPayload struct {
	SessionID int64 `json:"sessionId"`
	// DiaryUpdatedAt é a "versão" do diário que pediu a geração: entra na
	// chave de idempotência, então uma task reentregue pelo asynq não gera
	// duas vezes, mas um diário salvo de novo gera de novo.
	DiaryUpdatedAt time.Time `json:"diaryUpdatedAt"`
}

type posaulaCorrigirPayload struct {
	AnswerID int64 `json:"answerId"`
}

func posaulaTaskIDGerar(sessionID int64) string { return fmt.Sprintf("posaula:gerar:%d", sessionID) }
func posaulaTaskIDCorrigir(answerID int64) string {
	return fmt.Sprintf("posaula:corrigir:%d", answerID)
}

// posaulaTaskIDGerarVersao é o id da SEGUNDA geração de uma aula, usado só
// quando a task base está ativa (ver posaulaEnfileirarComConflito): leva a
// versão do diário, então dois saves da mesma versão continuam colidindo.
func posaulaTaskIDGerarVersao(sessionID int64, versao time.Time) string {
	return fmt.Sprintf("posaula:gerar:%d:v%d", sessionID, versao.UnixNano())
}

// posaulaCtxFinal é o ctx das gravações de estado FINAL (failed, ok
// tardio, fallback da correção): sem o cancelamento do ctx do asynq — que
// pode já ter estourado o Timeout quando a task chega aqui — e com prazo
// próprio curto. Sem isto o diário ficava preso em running pra sempre.
func posaulaCtxFinal(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), posaulaEstadoFinalTimeout)
}

// posaulaUltimaTentativa diz se esta entrega da task é a última que o asynq
// vai fazer (retentativas esgotadas). Fora do asynq — sem os contadores no
// ctx — não existe retentativa nenhuma, então é sempre a última.
func posaulaUltimaTentativa(ctx context.Context) bool {
	n, ok1 := asynq.GetRetryCount(ctx)
	max, ok2 := asynq.GetMaxRetry(ctx)
	if !ok1 || !ok2 {
		return true
	}
	return n >= max
}

// posaulaIdempotencyKey — sha256(sessão | versão do diário | versão do prompt).
func posaulaIdempotencyKey(sessionID int64, diaryUpdatedAt time.Time) string {
	return sha256Hex(fmt.Sprintf("%d|%s|%s", sessionID, diaryUpdatedAt.UTC().Format(time.RFC3339Nano), posaulaPromptVersion))
}

// errFilaIndisponivel: sem asynq (modo degradado/teste) a geração não tem
// como acontecer — o handler avisa em vez de fingir que enfileirou.
var errFilaIndisponivel = errors.New("fila de tarefas indisponível")

// ── Enfileirar ───────────────────────────────────────────────────────────────

// enqueuePosaulaGerar marca o diário como pending e enfileira a geração.
// TaskID fixo por aula: se já existe uma geração desta aula no asynq, o que
// acontece depende do estado dela (ver posaulaEnfileirarComConflito). A
// versão (updated_at do diário, ou o instante do pedido no regenerate)
// entra na chave de idempotência — é ela que impede gerar duas vezes a
// mesma versão mesmo quando duas tasks acabam rodando.
func (s *Server) enqueuePosaulaGerar(ctx context.Context, sessionID int64, diaryUpdatedAt time.Time) error {
	if s.queue == nil {
		return errFilaIndisponivel
	}
	b, err := json.Marshal(posaulaGerarPayload{SessionID: sessionID, DiaryUpdatedAt: diaryUpdatedAt})
	if err != nil {
		return err
	}
	if err := s.posaulaSetAIStatus(ctx, sessionID, "pending", nil); err != nil {
		return err
	}
	id := posaulaTaskIDGerar(sessionID)
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(TaskPosaulaGerar, b, posaulaOptsComID(id)...))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		err = s.posaulaEnfileirarComConflito(ctx, sessionID, id, b, diaryUpdatedAt)
	}
	if err != nil {
		msg := "não consegui enfileirar a geração: " + err.Error()
		_ = s.posaulaSetAIStatus(ctx, sessionID, "failed", &msg)
		return err
	}
	return nil
}

// posaulaOptsComID: as opções padrão mais o TaskID (e o que vier depois).
func posaulaOptsComID(id string, extra ...asynq.Option) []asynq.Option {
	opts := append(append([]asynq.Option{}, posaulaTaskOpts...), asynq.TaskID(id))
	return append(opts, extra...)
}

// posaulaEnfileirarComConflito decide o que fazer quando já existe uma task
// posaula:gerar desta aula segurando o id base:
//   - arquivada (esgotou as tentativas) ou concluída com retenção: o id está
//     "preso" sem ninguém trabalhando — solta e enfileira de novo com o id
//     base, senão "gerar de novo" nunca mais funcionaria pra essa aula;
//   - ATIVA: ela já leu o diário (posaulaCarregarContexto roda no início), e
//     a versão que acabou de ser salva ficaria perdida em silêncio, com as
//     práticas da versão antiga e ai_status=ok. Então entra uma SEGUNDA task,
//     com id versionado e agendada pra daqui a 30 s — quando rodar lê o
//     diário atual; a chave de idempotência impede gerar duas vezes a mesma
//     versão, e o soft-delete da gravação deixa só as práticas mais novas;
//   - pendente, agendada ou em retry: ainda não leu o diário — vai ler o
//     atual quando rodar; nada a fazer.
//
// Sem Redis pra consultar (ou consulta falhando) assume que tem alguém a
// caminho — o pior caso é o professor clicar em "Gerar de novo" mais tarde.
func (s *Server) posaulaEnfileirarComConflito(ctx context.Context, sessionID int64, id string, payload []byte, versao time.Time) error {
	state, ok := s.posaulaEstadoDaTask(id)
	if !ok {
		return nil
	}
	switch state {
	case asynq.TaskStateArchived, asynq.TaskStateCompleted:
		if !s.posaulaSoltarTask(id, state) {
			return nil
		}
		_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(TaskPosaulaGerar, payload, posaulaOptsComID(id)...))
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil // alguém enfileirou entre o delete e o enqueue — já tem uma a caminho
		}
		return err
	case asynq.TaskStateActive:
		idV := posaulaTaskIDGerarVersao(sessionID, versao)
		_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(TaskPosaulaGerar, payload, posaulaOptsComID(idV, asynq.ProcessIn(posaulaReenfileirarEm))...))
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil // esta versão já está agendada
		}
		if err == nil {
			slog.Info("posaula: diário salvo durante geração ativa — segunda geração agendada", "session", sessionID, "task", idV, "em", posaulaReenfileirarEm)
		}
		return err
	default:
		return nil
	}
}

// posaulaEstadoDaTask consulta o estado de uma task pelo id na fila do
// Pós-aula. ok=false sem Redis, task inexistente ou erro de consulta.
func (s *Server) posaulaEstadoDaTask(id string) (asynq.TaskState, bool) {
	if s.rdb == nil {
		return 0, false
	}
	insp := asynq.NewInspectorFromRedisClient(s.rdb)
	defer insp.Close()
	info, err := insp.GetTaskInfo(posaulaAsynqQueue, id)
	if err != nil {
		return 0, false
	}
	return info.State, true
}

// posaulaSoltarTask apaga do asynq uma task que já TERMINOU (arquivada ou
// concluída) e está só segurando o id. Devolve true se soltou.
func (s *Server) posaulaSoltarTask(id string, state asynq.TaskState) bool {
	insp := asynq.NewInspectorFromRedisClient(s.rdb)
	defer insp.Close()
	if err := insp.DeleteTask(posaulaAsynqQueue, id); err != nil {
		slog.Warn("posaula: não consegui soltar a task presa", "id", id, "state", state.String(), "err", err)
		return false
	}
	return true
}

// enqueuePosaulaCorrigir enfileira a correção de uma resposta aberta.
func (s *Server) enqueuePosaulaCorrigir(ctx context.Context, answerID int64) error {
	if s.queue == nil {
		return errFilaIndisponivel
	}
	b, err := json.Marshal(posaulaCorrigirPayload{AnswerID: answerID})
	if err != nil {
		return err
	}
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(TaskPosaulaCorrigir, b, posaulaOptsComID(posaulaTaskIDCorrigir(answerID))...))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}

// ── Estado no diário / ai_run ────────────────────────────────────────────────

func (s *Server) posaulaSetAIStatus(ctx context.Context, sessionID int64, status string, errMsg *string) error {
	_, err := s.portalDB.Exec(ctx, `UPDATE session_diary SET ai_status = $2, ai_error = $3, ai_updated_at = now() WHERE session_id = $1`,
		sessionID, status, errMsg)
	return err
}

// posaulaRun é uma linha de ai_run pronta pra gravar.
type posaulaRun struct {
	Key        *string // só a geração bem-sucedida tem chave (UNIQUE); falha vai sem, senão a retentativa conflita
	Kind       string
	SessionID  *int64
	AnswerID   *int64
	InputChars int
	OutputRaw  string
	Status     string
	Error      *string
	Duration   time.Duration
}

func posaulaInsertRunSQL() string {
	return `INSERT INTO ai_run (idempotency_key, kind, session_id, answer_id, model, prompt_version, input_chars, output_raw, status, error, duration_ms, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now()) RETURNING id`
}

func (s *Server) posaulaInsertRun(ctx context.Context, tx pgx.Tx, r posaulaRun) (int64, error) {
	var id int64
	args := []any{r.Key, r.Kind, r.SessionID, r.AnswerID, posaulaModelo, posaulaPromptVersion, r.InputChars, r.OutputRaw, r.Status, r.Error, r.Duration.Milliseconds()}
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, posaulaInsertRunSQL(), args...).Scan(&id)
	} else {
		err = s.portalDB.QueryRow(ctx, posaulaInsertRunSQL(), args...).Scan(&id)
	}
	return id, err
}

// posaulaRunOKExiste diz se essa versão do diário já foi gerada com sucesso.
func (s *Server) posaulaRunOKExiste(ctx context.Context, key string) (bool, error) {
	var ok bool
	err := s.portalDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_run WHERE idempotency_key = $1 AND status = 'ok')`, key).Scan(&ok)
	return ok, err
}

// ── Geração ──────────────────────────────────────────────────────────────────

// handlePosaulaGerar é o handler asynq. Payload corrompido não vai melhorar
// com retry (SkipRetry). Erro devolvido = asynq retenta (só o transitório
// chega aqui; falha de conteúdo termina em ai_status=failed e nil).
func (s *Server) handlePosaulaGerar(ctx context.Context, t *asynq.Task) error {
	var p posaulaGerarPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil || p.SessionID <= 0 {
		slog.Error("posaula: task de geração com payload inválido", "err", err)
		return asynq.SkipRetry
	}
	return s.posaulaGerar(ctx, p)
}

// posaulaContexto é tudo que a geração lê do banco antes de montar o brief.
type posaulaContexto struct {
	ClassID       int64
	Date          string
	ClassName     string
	CourseName    string
	Summary       string
	Attachments   []portalDiaryAttachmentDTO
	AuthorEmail   string
	Alunos        []posaulaAluno
	Anteriores    []briefDiario
	PraticasPrev  []briefPraticaAnterior
	temDiario     bool
	sessionExists bool
}

type posaulaAluno struct {
	ID                 int64
	Nome               string
	ConteudoContratado string
}

func (s *Server) posaulaCarregarContexto(ctx context.Context, sessionID int64) (*posaulaContexto, error) {
	c := &posaulaContexto{}
	var summary, attachments, authorEmail *string
	err := s.portalDB.QueryRow(ctx, `
		SELECT cs.class_id, to_char(cs.date,'YYYY-MM-DD'), COALESCE(cl.name,''), COALESCE(co.name,''),
		       sd.summary, sd.attachments::text, sd.author_email
		FROM class_session cs
		JOIN class cl ON cl.id = cs.class_id
		LEFT JOIN course co ON co.id = cl.course_id
		LEFT JOIN session_diary sd ON sd.session_id = cs.id
		WHERE cs.id = $1`, sessionID).
		Scan(&c.ClassID, &c.Date, &c.ClassName, &c.CourseName, &summary, &attachments, &authorEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil // sessionExists=false
	}
	if err != nil {
		return nil, err
	}
	c.sessionExists = true
	if summary == nil {
		return c, nil // temDiario=false
	}
	c.temDiario = true
	c.Summary = *summary
	if attachments != nil {
		c.Attachments = portalDiaryParseAttachments(*attachments)
	}
	if authorEmail != nil {
		c.AuthorEmail = *authorEmail
	}
	if c.ClassName == "" {
		c.ClassName = fmt.Sprintf("Turma %d", c.ClassID)
	}

	rows, err := s.portalDB.Query(ctx, `SELECT u.id, COALESCE(u.name,''), COALESCE(e.contracted_content,'')
		FROM enrollment e JOIN "user" u ON u.id = e.user_id
		WHERE e.class_id = $1 ORDER BY COALESCE(u.name,''), u.id`, c.ClassID)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var a posaulaAluno
		if err := rows.Scan(&a.ID, &a.Nome, &a.ConteudoContratado); err != nil {
			rows.Close()
			return nil, err
		}
		c.Alunos = append(c.Alunos, a)
		ids = append(ids, a.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Os 3 diários anteriores da mesma turma (por data da aula, mais recente
	// primeiro) — é o que permite a prática de revisão espaçada.
	prev, err := s.portalDB.Query(ctx, `
		SELECT to_char(cs.date,'YYYY-MM-DD'), sd.summary, sd.attachments::text
		FROM session_diary sd JOIN class_session cs ON cs.id = sd.session_id
		WHERE cs.class_id = $1 AND cs.id <> $2::int AND (cs.date, cs.id) < ($3::date, $2::int)
		ORDER BY cs.date DESC, cs.id DESC LIMIT 3`, c.ClassID, sessionID, c.Date)
	if err != nil {
		return nil, err
	}
	for prev.Next() {
		var d briefDiario
		var att string
		if err := prev.Scan(&d.Data, &d.Resumo, &att); err != nil {
			prev.Close()
			return nil, err
		}
		for _, a := range portalDiaryParseAttachments(att) {
			d.Anexos = append(d.Anexos, a.Name)
		}
		c.Anteriores = append(c.Anteriores, d)
	}
	prev.Close()
	if err := prev.Err(); err != nil {
		return nil, err
	}

	if len(ids) > 0 {
		pr, err := s.portalDB.Query(ctx, `
			SELECT COALESCE(u.name,''), pt.title, pa.is_correct
			FROM posaula_task pt
			LEFT JOIN "user" u ON u.id = pt.user_id
			LEFT JOIN posaula_answer pa ON pa.task_id = pt.id
			WHERE pt.user_id = ANY($1::bigint[]) AND pt.session_id <> $2 AND pt.deleted_at IS NULL
			ORDER BY pt.created_at DESC, pt.id DESC LIMIT $3`, ids, sessionID, posaulaPraticasAnterioresMax)
		if err != nil {
			return nil, err
		}
		for pr.Next() {
			var p briefPraticaAnterior
			if err := pr.Scan(&p.Aluno, &p.Titulo, &p.Correta); err != nil {
				pr.Close()
				return nil, err
			}
			c.PraticasPrev = append(c.PraticasPrev, p)
		}
		pr.Close()
		if err := pr.Err(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// posaulaLerAnexosTexto baixa do Drive os anexos que são texto e monta a
// lista que vai pro brief, respeitando o teto por arquivo e o total. Falha
// em um anexo não derruba a geração — o modelo fica sem aquele arquivo.
func (s *Server) posaulaLerAnexosTexto(ctx context.Context, anexos []portalDiaryAttachmentDTO) []briefAnexo {
	if s.drive == nil {
		return nil
	}
	var out []briefAnexo
	restante := posaulaAnexosMaxChars
	for _, a := range anexos {
		if restante <= 0 {
			break
		}
		if !posaulaAnexoEhTexto(a.Name) {
			continue
		}
		b, err := s.drive.DownloadBytes(ctx, a.DriveFileID, posaulaAnexoMaxBytes)
		if err != nil {
			slog.Warn("posaula: não consegui ler o anexo, segue sem ele", "file", a.DriveFileID, "name", a.Name, "err", err)
			continue
		}
		texto, substituiu := posaulaLimparUTF8(b, int64(len(b)) >= posaulaAnexoMaxBytes)
		if substituiu {
			slog.Warn("posaula: anexo com bytes fora do UTF-8 (Latin-1/cp1252?), trocados por �", "file", a.DriveFileID, "name", a.Name)
		}
		texto = strings.TrimSpace(texto)
		if texto == "" {
			continue
		}
		texto = truncarRunas(texto, restante)
		restante -= utf8.RuneCountInString(texto)
		out = append(out, briefAnexo{Nome: a.Name, Conteudo: texto})
	}
	return out
}

// posaulaLimparUTF8 deixa o conteúdo de um anexo seguro pra ir no brief (o
// Postgres recusa UTF-8 inválido em TEXT e o modelo não lê byte solto):
//   - truncado no limite de bytes (64 KB) → corta SÓ o último rune que
//     ficou pela metade, sem inventar � no fim de um arquivo que só era
//     grande demais;
//   - o resto → strings.ToValidUTF8, que troca cada sequência inválida por
//     um � e PRESERVA tudo depois dela. Antes um único 'ç' em Latin-1 no
//     começo de um .csv jogava o arquivo inteiro fora, em silêncio.
//
// substituiu = houve troca por � (o chamador loga o aviso).
func posaulaLimparUTF8(b []byte, truncado bool) (texto string, substituiu bool) {
	if truncado && len(b) > 0 {
		// Volta até o início do último rune (no máximo 3 bytes de continuação)
		// e, se ele não está completo, corta.
		i := len(b) - 1
		for i > 0 && len(b)-i < utf8.UTFMax && !utf8.RuneStart(b[i]) {
			i--
		}
		if !utf8.FullRune(b[i:]) {
			b = b[:i]
		}
	}
	if utf8.Valid(b) {
		return string(b), false
	}
	return strings.ToValidUTF8(string(b), "�"), true
}

// posaulaLiberacaoPara decide o available_from das práticas novas: se a
// aula JÁ tinha prática liberada (regeneração de algo que o aluno já vê),
// as novas liberam na hora — a gravação tira as antigas sem resposta do ar,
// e não há por que deixar o aluno 24 h sem nada dessa aula. Senão, o
// próximo 07:00 de sempre.
func posaulaLiberacaoPara(now time.Time, jaTinhaLiberada bool) time.Time {
	if jaTinhaLiberada {
		return now
	}
	return proximaLiberacao(now)
}

// posaulaSessaoTemPraticaLiberada: existe prática não excluída desta aula
// que o aluno já pode ver? (decide o available_from da regeneração)
func (s *Server) posaulaSessaoTemPraticaLiberada(ctx context.Context, sessionID int64) (bool, error) {
	var ok bool
	err := s.portalDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM posaula_task
		WHERE session_id = $1 AND deleted_at IS NULL AND available_from <= now())`, sessionID).Scan(&ok)
	return ok, err
}

// posaulaGerar é o miolo do handler.
func (s *Server) posaulaGerar(ctx context.Context, p posaulaGerarPayload) error {
	inicio := time.Now()
	key := posaulaIdempotencyKey(p.SessionID, p.DiaryUpdatedAt)
	if ok, err := s.posaulaRunOKExiste(ctx, key); err != nil {
		return fmt.Errorf("posaula: conferir idempotência: %w", err)
	} else if ok {
		// Task reentregue depois de já ter gravado: só garante que o estado
		// não ficou preso em pending/running.
		return s.posaulaConfirmarOK(ctx, p.SessionID)
	}
	if err := s.posaulaSetAIStatus(ctx, p.SessionID, "running", nil); err != nil {
		return fmt.Errorf("posaula: marcar running: %w", err)
	}
	c, err := s.posaulaCarregarContexto(ctx, p.SessionID)
	if err != nil {
		msg := "erro ao ler o diário: " + err.Error()
		s.posaulaSetAIStatusFinal(ctx, p.SessionID, "failed", &msg)
		return fmt.Errorf("posaula: carregar contexto: %w", err)
	}
	if !c.sessionExists {
		slog.Warn("posaula: aula não existe mais, geração descartada", "session", p.SessionID)
		return nil
	}
	if !c.temDiario {
		return s.posaulaFalhar(ctx, p.SessionID, "", "", "a aula não tem diário registrado", time.Since(inicio))
	}
	if len(c.Alunos) == 0 {
		return s.posaulaFalhar(ctx, p.SessionID, "", "", "nenhum aluno matriculado na turma desta aula", time.Since(inicio))
	}

	in := briefPraticasInput{
		Curso:              c.CourseName,
		Turma:              c.ClassName,
		DiariosAnteriores:  c.Anteriores,
		PraticasAnteriores: c.PraticasPrev,
		Diario:             briefDiario{Data: c.Date, Resumo: c.Summary},
		Anexos:             s.posaulaLerAnexosTexto(ctx, c.Attachments),
	}
	for _, a := range c.Alunos {
		in.Alunos = append(in.Alunos, briefAluno{Nome: a.Nome, ConteudoContratado: a.ConteudoContratado})
	}
	for _, a := range c.Attachments {
		in.Diario.Anexos = append(in.Diario.Anexos, a.Name)
	}

	// Chama o modelo; JSON inválido ganha UMA segunda chance com o erro no
	// brief. Erro de rede/5xx já foi retentado dentro de claudeRaw.
	brief := montarBriefPraticas(in)
	raw, err := s.claudeRaw(ctx, brief, posaulaModelo)
	if err != nil {
		return s.posaulaFalharChamada(ctx, p.SessionID, brief, err, time.Since(inicio))
	}
	out, perr := parsePraticas(raw)
	if perr != nil {
		slog.Warn("posaula: resposta inválida, pedindo de novo", "session", p.SessionID, "err", perr)
		in.ErroAnterior = perr.Error()
		brief = montarBriefPraticas(in)
		raw, err = s.claudeRaw(ctx, brief, posaulaModelo)
		if err != nil {
			return s.posaulaFalharChamada(ctx, p.SessionID, brief, err, time.Since(inicio))
		}
		out, perr = parsePraticas(raw)
		if perr != nil {
			return s.posaulaFalhar(ctx, p.SessionID, brief, raw, "resposta do Claude inválida: "+perr.Error(), time.Since(inicio))
		}
	}

	jaLiberada, err := s.posaulaSessaoTemPraticaLiberada(ctx, p.SessionID)
	if err != nil {
		msg := "erro ao conferir as práticas anteriores: " + err.Error()
		s.posaulaSetAIStatusFinal(ctx, p.SessionID, "failed", &msg)
		return fmt.Errorf("posaula: conferir práticas liberadas: %w", err)
	}
	liberacao := posaulaLiberacaoPara(time.Now(), jaLiberada)
	n, err := s.posaulaGravarPraticas(ctx, p.SessionID, key, c, out, brief, raw, liberacao, time.Since(inicio))
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			// Outra entrega da mesma task gravou primeiro. As práticas já
			// estão lá; só não deixa o running que ESTA entrega marcou.
			return s.posaulaConfirmarOK(ctx, p.SessionID)
		}
		msg := "erro ao gravar as práticas: " + err.Error()
		s.posaulaSetAIStatusFinal(ctx, p.SessionID, "failed", &msg)
		return fmt.Errorf("posaula: gravar: %w", err)
	}
	s.posaulaLogActivityBackground(ctx, c.AuthorEmail, "posaula_praticas_geradas", "session", fmt.Sprint(p.SessionID), map[string]any{
		"praticas": len(out.Praticas), "alunos": len(c.Alunos), "inseridas": n, "availableFrom": liberacao,
	})
	slog.Info("posaula: práticas geradas", "session", p.SessionID, "praticas", len(out.Praticas), "alunos", len(c.Alunos), "availableFrom", liberacao, "ms", time.Since(inicio).Milliseconds())
	return nil
}

// posaulaConfirmarOK: a geração desta versão já está gravada (por esta ou
// por outra entrega) — garante que o diário não ficou em pending/running.
func (s *Server) posaulaConfirmarOK(ctx context.Context, sessionID int64) error {
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	_, err := s.portalDB.Exec(fctx, `UPDATE session_diary SET ai_status = 'ok', ai_error = NULL, ai_updated_at = now()
		WHERE session_id = $1 AND ai_status IN ('pending','running')`, sessionID)
	return err
}

// posaulaSetAIStatusFinal é o posaulaSetAIStatus das gravações de estado
// final (ver posaulaCtxFinal); falha só loga — o chamador já está saindo
// com o erro de verdade.
func (s *Server) posaulaSetAIStatusFinal(ctx context.Context, sessionID int64, status string, errMsg *string) {
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	if err := s.posaulaSetAIStatus(fctx, sessionID, status, errMsg); err != nil {
		slog.Error("posaula: não consegui gravar o estado final do diário", "session", sessionID, "status", status, "err", err)
	}
}

// posaulaFalharChamada decide o que fazer quando o agent-go falhou. Erro
// transitório (rede/5xx/429) com retentativa sobrando: registra a chamada
// em ai_run, deixa o diário em pending com "Tentando de novo…" (o professor
// não vê "falhou" durante o backoff, e o estado efetivo continua contando
// do ai_updated_at) e devolve o erro pro asynq retentar. Na última
// tentativa, ou erro definitivo (sem secret, 4xx), grava failed de vez.
func (s *Server) posaulaFalharChamada(ctx context.Context, sessionID int64, brief string, err error, dur time.Duration) error {
	var transitorio agentTransientError
	if errors.As(err, &transitorio) && !posaulaUltimaTentativa(ctx) {
		fctx, cancel := posaulaCtxFinal(ctx)
		defer cancel()
		sid := sessionID
		msg := err.Error()
		if _, ierr := s.posaulaInsertRun(fctx, nil, posaulaRun{
			Kind: "praticas", SessionID: &sid, InputChars: utf8.RuneCountInString(brief), Status: "failed", Error: &msg, Duration: dur,
		}); ierr != nil {
			slog.Warn("posaula: não consegui gravar o ai_run da tentativa", "session", sessionID, "err", ierr)
		}
		tentando := posaulaErroTentandoDeNovo
		if serr := s.posaulaSetAIStatus(fctx, sessionID, "pending", &tentando); serr != nil {
			slog.Warn("posaula: não consegui marcar a retentativa no diário", "session", sessionID, "err", serr)
		}
		slog.Warn("posaula: chamada ao Claude falhou, o asynq vai retentar", "session", sessionID, "err", msg)
		return err
	}
	if ferr := s.posaulaFalhar(ctx, sessionID, brief, "", err.Error(), dur); ferr != nil {
		return ferr
	}
	if errors.As(err, &transitorio) {
		return err // última tentativa: o asynq arquiva a task (o estado já é failed)
	}
	return nil
}

// posaulaFalhar registra a falha DEFINITIVA: ai_run failed (sem chave) +
// diário failed com a mensagem, que a tela do professor mostra como está.
// Usa o ctx final: o do asynq pode já ter sido cancelado pelo Timeout.
func (s *Server) posaulaFalhar(ctx context.Context, sessionID int64, brief, raw, msg string, dur time.Duration) error {
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	sid := sessionID
	if _, err := s.posaulaInsertRun(fctx, nil, posaulaRun{
		Kind: "praticas", SessionID: &sid, InputChars: utf8.RuneCountInString(brief), OutputRaw: raw, Status: "failed", Error: &msg, Duration: dur,
	}); err != nil {
		slog.Warn("posaula: não consegui gravar o ai_run da falha", "session", sessionID, "err", err)
	}
	if err := s.posaulaSetAIStatus(fctx, sessionID, "failed", &msg); err != nil {
		return fmt.Errorf("posaula: marcar failed: %w", err)
	}
	slog.Warn("posaula: geração falhou", "session", sessionID, "err", msg)
	return nil
}

// posaulaGravarPraticas grava tudo numa transação: as práticas antigas SEM
// resposta saem do ar (regeneração substitui só o que ninguém respondeu),
// entram as novas pra cada aluno matriculado, o diário ganha o resumo do
// aluno e o estado ok, e o ai_run ok com a chave de idempotência. Devolve
// quantas práticas foram inseridas (alunos × práticas).
func (s *Server) posaulaGravarPraticas(ctx context.Context, sessionID int64, key string, c *posaulaContexto, out praticasOut, brief, raw string, liberacao time.Time, dur time.Duration) (int, error) {
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	sid := sessionID
	runID, err := s.posaulaInsertRun(ctx, tx, posaulaRun{
		Key: &key, Kind: "praticas", SessionID: &sid, InputChars: utf8.RuneCountInString(brief), OutputRaw: raw, Status: "ok", Duration: dur,
	})
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE posaula_task SET deleted_at = now()
		WHERE session_id = $1 AND deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM posaula_answer pa WHERE pa.task_id = posaula_task.id)`, sessionID); err != nil {
		return 0, err
	}
	n := 0
	for _, aluno := range c.Alunos {
		for _, p := range out.Praticas {
			opcoes, err := json.Marshal(p.Opcoes)
			if err != nil {
				return 0, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO posaula_task (session_id, user_id, title, statement, kind, options, answer_key, hint, difficulty, available_from, ai_run_id, created_at)
				VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, now())`,
				sessionID, aluno.ID, p.Titulo, p.Enunciado, p.Tipo, string(opcoes), string(p.Gabarito), p.Dica, p.Dificuldade, liberacao, runID); err != nil {
				return 0, err
			}
			n++
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE session_diary SET student_summary = $2, ai_status = 'ok', ai_error = NULL, ai_updated_at = now() WHERE session_id = $1`,
		sessionID, out.ResumoAluno); err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// posaulaLogActivityBackground é o portalLogActivity de quem não tem
// *http.Request (worker). O ator é o autor do diário, resolvido pelo e-mail
// no banco de AUTH (s.userByEmail) — logs.user_id é o id do auth em todas
// as outras linhas (portalLogActivity grava userIDFrom(r)), e o id do
// "user" do Portal é outro espaço de números; misturar os dois quebrava o
// filtro por ator da tela de Logs. Sem conta no auth não grava, só loga.
// Roda depois do trabalho principal, então usa o ctx final.
func (s *Server) posaulaLogActivityBackground(ctx context.Context, authorEmail, action, entityType, entityID string, metadata map[string]any) {
	email := strings.ToLower(strings.TrimSpace(authorEmail))
	if email == "" || s.db == nil {
		slog.Info("activity log (posaula): sem autor pra atribuir, não gravado", "action", action, "entityId", entityID)
		return
	}
	fctx, cancel := posaulaCtxFinal(ctx)
	defer cancel()
	u, err := s.userByEmail(fctx, email)
	if err != nil {
		slog.Warn("activity log (posaula): não consegui resolver o autor no auth", "err", err, "action", action)
		return
	}
	if u == nil {
		slog.Info("activity log (posaula): autor sem conta no auth, não gravado", "action", action, "entityId", entityID)
		return
	}
	payload := map[string]any{"actor": map[string]any{"id": fmt.Sprint(u.ID), "email": email, "name": u.Name}, "entityId": entityID, "metadata": metadata}
	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if _, err := s.portalDB.Exec(fctx, `INSERT INTO logs (user_id, "Message", action, entity_name, "LogDate") VALUES ($1,$2,$3,$4,NOW())`,
		u.ID, string(rawJSON), action, entityType); err != nil {
		slog.Warn("activity log (posaula): insert falhou", "err", err, "action", action)
	}
}

// ── Correção de resposta aberta ──────────────────────────────────────────────

func (s *Server) handlePosaulaCorrigir(ctx context.Context, t *asynq.Task) error {
	var p posaulaCorrigirPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil || p.AnswerID <= 0 {
		slog.Error("posaula: task de correção com payload inválido", "err", err)
		return asynq.SkipRetry
	}
	return s.posaulaCorrigir(ctx, p.AnswerID)
}

// posaulaCorrigir pede o veredito ao Claude e grava. Falha DEFINITIVA do
// modelo (sem secret, 4xx, JSON inválido duas vezes) termina no fallback "o
// professor vai olhar". Falha transitória (rede, 5xx, 429) volta pro asynq
// retentar com backoff enquanto houver tentativa — o agent-go reiniciando
// por 30 s não pode virar um fallback permanente. Na ÚLTIMA tentativa
// qualquer erro (do modelo ou de banco) grava o fallback antes de sair: o
// aluno nunca fica com a resposta sem correção pra sempre.
func (s *Server) posaulaCorrigir(ctx context.Context, answerID int64) (err error) {
	inicio := time.Now()
	ultima := posaulaUltimaTentativa(ctx)
	defer func() {
		if err == nil || !ultima {
			return
		}
		// O asynq vai arquivar a task depois deste retorno — é agora ou nunca.
		fctx, cancel := posaulaCtxFinal(ctx)
		defer cancel()
		if ferr := s.posaulaMarcarCorrecaoFalhou(fctx, answerID, nil); ferr != nil {
			slog.Error("posaula: última tentativa da correção falhou E o fallback não gravou", "answer", answerID, "err", err, "fallbackErr", ferr)
		}
	}()
	var (
		taskID      int64
		answerText  *string
		correctedAt *time.Time
		in          briefCorrecaoInput
	)
	err = s.portalDB.QueryRow(ctx, `
		SELECT pa.task_id, pa.answer_text, pa.corrected_at, pt.title, pt.statement, pt.answer_key, pt.hint, COALESCE(co.name,'')
		FROM posaula_answer pa
		JOIN posaula_task pt ON pt.id = pa.task_id
		JOIN class_session cs ON cs.id = pt.session_id
		JOIN class cl ON cl.id = cs.class_id
		LEFT JOIN course co ON co.id = cl.course_id
		WHERE pa.id = $1`, answerID).
		Scan(&taskID, &answerText, &correctedAt, &in.Titulo, &in.Enunciado, &in.Gabarito, &in.Dica, &in.Curso)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // resposta sumiu (aula apagada) — nada a corrigir
	}
	if err != nil {
		return fmt.Errorf("posaula: carregar resposta: %w", err)
	}
	if correctedAt != nil {
		return nil // já corrigida (task reentregue)
	}
	if answerText != nil {
		in.Resposta = *answerText
	}

	// falhar grava o fallback (ai_run failed + "o professor vai olhar") com
	// o ctx final — o do asynq pode ter sido cancelado pelo Timeout.
	falhar := func(brief, raw, msg string) error {
		fctx, cancel := posaulaCtxFinal(ctx)
		defer cancel()
		aid := answerID
		runID, err := s.posaulaInsertRun(fctx, nil, posaulaRun{
			Kind: "correcao", AnswerID: &aid, InputChars: utf8.RuneCountInString(brief), OutputRaw: raw, Status: "failed", Error: &msg, Duration: time.Since(inicio),
		})
		var runPtr *int64
		if err == nil {
			runPtr = &runID
		} else {
			slog.Warn("posaula: não consegui gravar o ai_run da correção", "answer", answerID, "err", err)
		}
		slog.Warn("posaula: correção automática falhou, caindo no fallback", "answer", answerID, "err", msg)
		if err := s.posaulaMarcarCorrecaoFalhou(fctx, answerID, runPtr); err != nil {
			return fmt.Errorf("posaula: marcar fallback da correção: %w", err)
		}
		return nil
	}
	// falhaDoModelo: transitório com retentativa sobrando volta pro asynq
	// (sem fallback ainda); o resto cai no fallback agora.
	falhaDoModelo := func(brief string, cerr error) error {
		var transitorio agentTransientError
		if errors.As(cerr, &transitorio) && !ultima {
			slog.Warn("posaula: correção falhou de forma transitória, o asynq vai retentar", "answer", answerID, "err", cerr)
			return cerr
		}
		return falhar(brief, "", cerr.Error())
	}

	brief := montarBriefCorrecao(in)
	raw, cerr := s.claudeRaw(ctx, brief, posaulaModelo)
	if cerr != nil {
		return falhaDoModelo(brief, cerr)
	}
	out, perr := parseCorrecao(raw)
	if perr != nil {
		in.ErroAnterior = perr.Error()
		brief = montarBriefCorrecao(in)
		raw, cerr = s.claudeRaw(ctx, brief, posaulaModelo)
		if cerr != nil {
			return falhaDoModelo(brief, cerr)
		}
		if out, perr = parseCorrecao(raw); perr != nil {
			return falhar(brief, raw, "resposta do Claude inválida: "+perr.Error())
		}
	}

	aid := answerID
	runID, err := s.posaulaInsertRun(ctx, nil, posaulaRun{
		Kind: "correcao", AnswerID: &aid, InputChars: utf8.RuneCountInString(brief), OutputRaw: raw, Status: "ok", Duration: time.Since(inicio),
	})
	if err != nil {
		return fmt.Errorf("posaula: gravar ai_run da correção: %w", err)
	}
	if _, err := s.portalDB.Exec(ctx, `UPDATE posaula_answer SET is_correct = $2, feedback = $3, corrected_at = now(), ai_run_id = $4
		WHERE id = $1 AND corrected_at IS NULL`, answerID, out.Correto, out.Feedback, runID); err != nil {
		return fmt.Errorf("posaula: gravar correção: %w", err)
	}
	return nil
}
