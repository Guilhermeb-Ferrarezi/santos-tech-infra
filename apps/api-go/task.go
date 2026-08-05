package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type TaskCategory struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
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
	CreatedBy       *int64     `json:"createdBy"`
	CreatedByNome   string     `json:"createdByNome"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type TaskInput struct {
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	CategoryID    *string    `json:"categoryId"`
	Status        string     `json:"status"`
	Priority      string     `json:"priority"`
	DueDate       *time.Time `json:"dueDate"`
	ResponsavelID *int64     `json:"responsavelId"`
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
	created_by, COALESCE((SELECT name FROM users WHERE id = created_by), ''),
	created_at, updated_at`

func scanTask(row pgx.Row) (*Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.CategoryID, &t.CategoryName,
		&t.Status, &t.Priority, &t.DueDate, &t.ResponsavelID, &t.ResponsavelNome,
		&t.CreatedBy, &t.CreatedByNome, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}

// listTasks: admin vê tudo; staff comum só vê onde é responsável OU criador
// (diferente de social_posts — aqui a visibilidade é enforced no servidor, não
// é um pré-filtro de conveniência client-side).
func (s *Server) listTasks(ctx context.Context, requesterID int64, isAdmin bool) ([]Task, error) {
	var rows pgx.Rows
	var err error
	if isAdmin {
		rows, err = s.db.Query(ctx, `SELECT `+taskCols+` FROM tasks ORDER BY COALESCE(due_date, created_at) DESC`)
	} else {
		rows, err = s.db.Query(ctx,
			`SELECT `+taskCols+` FROM tasks WHERE responsavel_id=$1 OR created_by=$1 ORDER BY COALESCE(due_date, created_at) DESC`,
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

func (s *Server) insertTask(ctx context.Context, in TaskInput, createdBy int64) (*Task, error) {
	task, err := scanTask(s.db.QueryRow(ctx, `
		INSERT INTO tasks (title, description, category_id, status, priority, due_date, responsavel_id, created_by)
		VALUES ($1,$2,$3::uuid,$4,$5,$6,$7,$8)
		RETURNING `+taskCols,
		in.Title, in.Description, in.CategoryID, in.Status, in.Priority, in.DueDate, in.ResponsavelID, createdBy))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return task, nil
}

func (s *Server) updateTask(ctx context.Context, id string, in TaskInput) (*Task, error) {
	task, err := scanTask(s.db.QueryRow(ctx, `
		UPDATE tasks SET
			title=$2, description=$3, category_id=$4::uuid, status=$5, priority=$6,
			due_date=$7, responsavel_id=$8, updated_at=now()
		WHERE id=$1::uuid
		RETURNING `+taskCols,
		id, in.Title, in.Description, in.CategoryID, in.Status, in.Priority, in.DueDate, in.ResponsavelID))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return task, nil
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
