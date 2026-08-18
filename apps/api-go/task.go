package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type TaskCategory struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// TaskCoResponsavel: pessoa adicional dona da mesma entrega (tarefa em dupla/
// conjunta). Cada uma confirma a própria parte — self-service, não delegável
// (exceto admin). ConfirmedAt nulo = ainda não confirmou.
type TaskCoResponsavel struct {
	UserID      int64      `json:"userId"`
	Nome        string     `json:"nome"`
	ConfirmedAt *time.Time `json:"confirmedAt"`
}

type Task struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	CategoryID      *string    `json:"categoryId"`
	CategoryName    string     `json:"categoryName"`
	Status          string     `json:"status"`
	Priority        string     `json:"priority"`
	DueDate         *time.Time `json:"dueDate"`
	ResponsavelID   *int64     `json:"responsavelId"`
	ResponsavelNome string     `json:"responsavelNome"`
	// ResponsavelConfirmedAt: confirmação do responsável "âncora" quando a
	// tarefa é conjunta (tem 1+ CoResponsaveis). Nulo = ainda não confirmou.
	// Em tarefa comum (sem co-responsáveis) o campo é irrelevante — nunca
	// entra na trava de conclusão.
	ResponsavelConfirmedAt *time.Time `json:"responsavelConfirmedAt"`
	// CoResponsaveis: vazio = tarefa comum, sem trava de confirmação na
	// conclusão. 1+ = tarefa conjunta — status "concluida" só é aceito quando
	// o responsável e todo co-responsável já confirmaram a própria parte.
	CoResponsaveis []TaskCoResponsavel `json:"coResponsaveis"`
	CreatedBy      *int64              `json:"createdBy"`
	CreatedByNome  string              `json:"createdByNome"`
	CreatedAt      time.Time           `json:"createdAt"`
	UpdatedAt      time.Time           `json:"updatedAt"`
}

type TaskInput struct {
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	CategoryID       *string    `json:"categoryId"`
	Status           string     `json:"status"`
	Priority         string     `json:"priority"`
	DueDate          *time.Time `json:"dueDate"`
	ResponsavelID    *int64     `json:"responsavelId"`
	CoResponsavelIDs []int64    `json:"coResponsavelIds"`
}

type TaskNote struct {
	ID         int64     `json:"id"`
	TaskID     string    `json:"taskId"`
	AuthorID   *int64    `json:"authorId"`
	AuthorName string    `json:"authorName"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"createdAt"`
}

var validTaskStatuses = map[string]bool{
	"a_fazer": true, "em_andamento": true, "concluida": true, "cancelada": true,
}
var validTaskPriorities = map[string]bool{
	"baixa": true, "media": true, "alta": true,
}

const taskCols = `id::text, title, description, category_id::text,
	COALESCE((SELECT name FROM task_categories WHERE id = category_id), ''),
	status, priority, due_date, responsavel_id,
	COALESCE((SELECT name FROM users WHERE id = responsavel_id), ''),
	responsavel_confirmed_at,
	COALESCE((SELECT array_agg(cr.user_id ORDER BY cr.id) FROM task_co_responsaveis cr WHERE cr.task_id = tasks.id), '{}'),
	COALESCE((SELECT array_agg(u.name ORDER BY cr.id) FROM task_co_responsaveis cr JOIN users u ON u.id = cr.user_id WHERE cr.task_id = tasks.id), '{}'),
	COALESCE((SELECT array_agg(cr.confirmed_at ORDER BY cr.id) FROM task_co_responsaveis cr WHERE cr.task_id = tasks.id), '{}'),
	created_by, COALESCE((SELECT name FROM users WHERE id = created_by), ''),
	created_at, updated_at`

func scanTask(row pgx.Row) (*Task, error) {
	var t Task
	var crIDs []int64
	var crNames []string
	var crConfirmedAts []*time.Time
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.CategoryID, &t.CategoryName,
		&t.Status, &t.Priority, &t.DueDate, &t.ResponsavelID, &t.ResponsavelNome, &t.ResponsavelConfirmedAt,
		&crIDs, &crNames, &crConfirmedAts,
		&t.CreatedBy, &t.CreatedByNome, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.CoResponsaveis = make([]TaskCoResponsavel, len(crIDs))
	for i, id := range crIDs {
		t.CoResponsaveis[i] = TaskCoResponsavel{UserID: id, Nome: crNames[i], ConfirmedAt: crConfirmedAts[i]}
	}
	return &t, nil
}

// listTasks: admin vê tudo; staff comum só vê onde é responsável, criador OU
// co-responsável (diferente de social_posts — aqui a visibilidade é enforced
// no servidor, não é um pré-filtro de conveniência client-side).
func (s *Server) listTasks(ctx context.Context, requesterID int64, isAdmin bool) ([]Task, error) {
	var rows pgx.Rows
	var err error
	if isAdmin {
		rows, err = s.db.Query(ctx, `SELECT `+taskCols+` FROM tasks ORDER BY COALESCE(due_date, created_at) DESC`)
	} else {
		rows, err = s.db.Query(ctx,
			`SELECT `+taskCols+` FROM tasks
			 WHERE responsavel_id=$1 OR created_by=$1
			    OR EXISTS (SELECT 1 FROM task_co_responsaveis cr WHERE cr.task_id = tasks.id AND cr.user_id = $1)
			 ORDER BY COALESCE(due_date, created_at) DESC`,
			requesterID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Server) getTask(ctx context.Context, id string) (*Task, error) {
	return scanTask(s.db.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = $1::uuid`, id))
}

// syncTaskCoResponsaveis alinha task_co_responsaveis com a lista desejada:
// remove quem saiu (isso também apaga a confirmação dele, de propósito — se a
// composição da dupla muda, a confirmação antiga não faz mais sentido) e
// insere quem entrou (confirmed_at começa nulo, via ON CONFLICT DO NOTHING).
// Quem já estava e continua não é tocado — preserva a confirmação existente.
func syncTaskCoResponsaveis(ctx context.Context, tx pgx.Tx, taskID string, userIDs []int64) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM task_co_responsaveis WHERE task_id=$1::uuid AND NOT (user_id = ANY($2::bigint[]))`,
		taskID, userIDs); err != nil {
		return err
	}
	if len(userIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO task_co_responsaveis (task_id, user_id)
		SELECT $1::uuid, uid FROM unnest($2::bigint[]) AS uid
		ON CONFLICT (task_id, user_id) DO NOTHING`,
		taskID, userIDs)
	return err
}

var errTaskNotConfirmed = appErr(http.StatusBadRequest, "TASK_NOT_CONFIRMED",
	"Não é possível concluir — todos os envolvidos precisam confirmar a própria parte antes de marcar como concluída.")

// checkTaskConfirmationsComplete impõe a trava de conclusão de tarefa
// conjunta: só permite concluir se o responsável (âncora) e todo
// co-responsável já confirmaram a própria parte. Tarefa comum (sem
// co-responsáveis) nunca trava.
func checkTaskConfirmationsComplete(t *Task) error {
	if len(t.CoResponsaveis) == 0 {
		return nil
	}
	if t.ResponsavelConfirmedAt == nil {
		return errTaskNotConfirmed
	}
	for _, cr := range t.CoResponsaveis {
		if cr.ConfirmedAt == nil {
			return errTaskNotConfirmed
		}
	}
	return nil
}

func (s *Server) insertTask(ctx context.Context, in TaskInput, createdBy int64) (*Task, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks (title, description, category_id, status, priority, due_date, responsavel_id, created_by)
		VALUES ($1,$2,$3::uuid,$4,$5,$6,$7,$8)
		RETURNING id::text`,
		in.Title, in.Description, in.CategoryID, in.Status, in.Priority, in.DueDate, in.ResponsavelID, createdBy).Scan(&id)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if err := syncTaskCoResponsaveis(ctx, tx, id, in.CoResponsavelIDs); err != nil {
		return nil, portalDBErr(err)
	}
	task, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=$1::uuid`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return task, nil
}

// updateTask recebe currentStatus (o status ANTES desta atualização, buscado
// pelo chamador) pra decidir se é uma transição de verdade pra "concluida" —
// mesma convenção do checklist de publicação do social. A trava é checada
// DENTRO da transação, contra o estado já sincronizado (depois do sync de
// co-responsáveis), pra pegar até o caso de alguém entrar na dupla e tentar
// concluir no mesmo save.
func (s *Server) updateTask(ctx context.Context, id string, in TaskInput, updatedBy int64, currentStatus string) (*Task, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE tasks SET
			title=$2, description=$3, category_id=$4::uuid, status=$5, priority=$6,
			due_date=$7, responsavel_id=$8, updated_at=now(),
			responsavel_confirmed_at = CASE WHEN responsavel_id IS DISTINCT FROM $8 THEN NULL ELSE responsavel_confirmed_at END
		WHERE id=$1::uuid`,
		id, in.Title, in.Description, in.CategoryID, in.Status, in.Priority, in.DueDate, in.ResponsavelID); err != nil {
		return nil, portalDBErr(err)
	}
	if err := syncTaskCoResponsaveis(ctx, tx, id, in.CoResponsavelIDs); err != nil {
		return nil, portalDBErr(err)
	}
	task, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=$1::uuid`, id))
	if err != nil {
		return nil, err
	}
	if in.Status == "concluida" && currentStatus != "concluida" {
		if err := checkTaskConfirmationsComplete(task); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return task, nil
}

var errTaskNotInvolved = appErr(http.StatusForbidden, "TASK_NOT_INVOLVED", "Você não é responsável por esta tarefa")

// setTaskConfirmation grava/desfaz a confirmação de targetUserID (responsável
// ou co-responsável) nesta tarefa. Tenta primeiro como responsável âncora
// (tasks.responsavel_confirmed_at); se ninguém bateu, tenta como
// co-responsável (task_co_responsaveis.confirmed_at). Se targetUserID não é
// nenhum dos dois, devolve errTaskNotInvolved.
func (s *Server) setTaskConfirmation(ctx context.Context, taskID string, targetUserID int64, confirmed bool) (*Task, error) {
	var ts *time.Time
	if confirmed {
		now := time.Now().UTC()
		ts = &now
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE tasks SET responsavel_confirmed_at=$3 WHERE id=$1::uuid AND responsavel_id=$2`,
		taskID, targetUserID, ts)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		tag2, err := s.db.Exec(ctx,
			`UPDATE task_co_responsaveis SET confirmed_at=$3 WHERE task_id=$1::uuid AND user_id=$2`,
			taskID, targetUserID, ts)
		if err != nil {
			return nil, err
		}
		if tag2.RowsAffected() == 0 {
			return nil, errTaskNotInvolved
		}
	}
	return s.getTask(ctx, taskID)
}

func (s *Server) deleteTask(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM tasks WHERE id=$1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errTaskNotFound
	}
	return nil
}

func (s *Server) listTaskNotes(ctx context.Context, taskID string) ([]TaskNote, error) {
	rows, err := s.db.Query(ctx, `
		SELECT n.id, n.task_id::text, n.author_id, COALESCE(u.name,''), n.content, n.created_at
		FROM task_notes n
		LEFT JOIN users u ON u.id = n.author_id
		WHERE n.task_id = $1::uuid ORDER BY n.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskNote{}
	for rows.Next() {
		var n TaskNote
		if err := rows.Scan(&n.ID, &n.TaskID, &n.AuthorID, &n.AuthorName, &n.Content, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Server) insertTaskNote(ctx context.Context, taskID string, authorID int64, content string) (*TaskNote, error) {
	var n TaskNote
	err := s.db.QueryRow(ctx, `
		INSERT INTO task_notes (task_id, author_id, content)
		VALUES ($1::uuid, $2, $3)
		RETURNING id, task_id::text, author_id,
		          (SELECT COALESCE(name,'') FROM users WHERE id=author_id),
		          content, created_at`,
		taskID, authorID, content).
		Scan(&n.ID, &n.TaskID, &n.AuthorID, &n.AuthorName, &n.Content, &n.CreatedAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &n, nil
}

// ── Categorias (CRUD admin-only) ────────────────────────────────────────────

const taskCategoryCols = `id::text, name, created_at`

func scanTaskCategory(row pgx.Row) (*TaskCategory, error) {
	var c TaskCategory
	err := row.Scan(&c.ID, &c.Name, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

func (s *Server) listTaskCategories(ctx context.Context) ([]TaskCategory, error) {
	rows, err := s.db.Query(ctx, `SELECT `+taskCategoryCols+` FROM task_categories ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskCategory{}
	for rows.Next() {
		c, err := scanTaskCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Server) insertTaskCategory(ctx context.Context, name string) (*TaskCategory, error) {
	cat, err := scanTaskCategory(s.db.QueryRow(ctx,
		`INSERT INTO task_categories (name) VALUES ($1) RETURNING `+taskCategoryCols, name))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return cat, nil
}

func (s *Server) updateTaskCategory(ctx context.Context, id, name string) (*TaskCategory, error) {
	cat, err := scanTaskCategory(s.db.QueryRow(ctx,
		`UPDATE task_categories SET name=$2 WHERE id=$1::uuid RETURNING `+taskCategoryCols, id, name))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return cat, nil
}

// deleteTaskCategory apaga a categoria; tarefas que a usavam ficam com
// category_id NULL (ON DELETE SET NULL na FK) — nunca trava a exclusão.
func (s *Server) deleteTaskCategory(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM task_categories WHERE id=$1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errTaskCategoryNotFound
	}
	return nil
}
