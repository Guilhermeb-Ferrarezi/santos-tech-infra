package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type LinkShowcaseItem struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	ImageURL    *string   `json:"imageUrl"`
	URL         string    `json:"url"`
	Status      string    `json:"status"`
	Ordem       int       `json:"ordem"`
	CreatedBy   *int64    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type LinkShowcaseItemInput struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	ImageURL    *string `json:"imageUrl"`
	URL         string  `json:"url"`
	Status      string  `json:"status"`
	Ordem       int     `json:"ordem"`
}

// LinkShowcasePublicItem é o que a rota pública expõe — nunca inclui status,
// createdBy ou timestamps (só o necessário pra renderizar um card).
type LinkShowcasePublicItem struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	ImageURL    *string `json:"imageUrl"`
	URL         string  `json:"url"`
	Ordem       int     `json:"ordem"`
}

var validLinkShowcaseStatuses = map[string]bool{
	"active": true, "inactive": true,
}

func validateLinkShowcaseInput(in *LinkShowcaseItemInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return validationErr("Título obrigatório")
	}
	in.URL = strings.TrimSpace(in.URL)
	parsed, err := url.ParseRequestURI(in.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return validationErr("URL de destino inválida (use http:// ou https://)")
	}
	if !validLinkShowcaseStatuses[in.Status] {
		return validationErr("Status inválido")
	}
	return nil
}

func toPublicLinkShowcaseView(i LinkShowcaseItem) LinkShowcasePublicItem {
	return LinkShowcasePublicItem{
		ID: i.ID, Title: i.Title, Description: i.Description,
		ImageURL: i.ImageURL, URL: i.URL, Ordem: i.Ordem,
	}
}

const linkShowcaseCols = `id::text, title, description, image_url, url, status, ordem, created_by, created_at, updated_at`

func scanLinkShowcaseItem(row pgx.Row) (*LinkShowcaseItem, error) {
	var i LinkShowcaseItem
	err := row.Scan(&i.ID, &i.Title, &i.Description, &i.ImageURL, &i.URL,
		&i.Status, &i.Ordem, &i.CreatedBy, &i.CreatedAt, &i.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &i, err
}

// listLinkShowcaseItems: admin — todos os cards, inclusive inactive.
func (s *Server) listLinkShowcaseItems(ctx context.Context) ([]LinkShowcaseItem, error) {
	rows, err := s.db.Query(ctx, `SELECT `+linkShowcaseCols+` FROM link_showcase_items ORDER BY ordem, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LinkShowcaseItem{}
	for rows.Next() {
		i, err := scanLinkShowcaseItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// listPublicLinkShowcaseItems: só os cards ativos, na ordem de exibição.
func (s *Server) listPublicLinkShowcaseItems(ctx context.Context) ([]LinkShowcaseItem, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+linkShowcaseCols+` FROM link_showcase_items WHERE status='active' ORDER BY ordem, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LinkShowcaseItem{}
	for rows.Next() {
		i, err := scanLinkShowcaseItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

func (s *Server) getLinkShowcaseItem(ctx context.Context, id string) (*LinkShowcaseItem, error) {
	return scanLinkShowcaseItem(s.db.QueryRow(ctx, `SELECT `+linkShowcaseCols+` FROM link_showcase_items WHERE id = $1::uuid`, id))
}

func (s *Server) insertLinkShowcaseItem(ctx context.Context, in LinkShowcaseItemInput, createdBy int64) (*LinkShowcaseItem, error) {
	item, err := scanLinkShowcaseItem(s.db.QueryRow(ctx, `
		INSERT INTO link_showcase_items (title, description, image_url, url, status, ordem, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING `+linkShowcaseCols,
		in.Title, in.Description, in.ImageURL, in.URL, in.Status, in.Ordem, createdBy))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return item, nil
}

func (s *Server) updateLinkShowcaseItem(ctx context.Context, id string, in LinkShowcaseItemInput) (*LinkShowcaseItem, error) {
	item, err := scanLinkShowcaseItem(s.db.QueryRow(ctx, `
		UPDATE link_showcase_items SET
			title=$2, description=$3, image_url=$4, url=$5, status=$6, ordem=$7, updated_at=now()
		WHERE id=$1::uuid
		RETURNING `+linkShowcaseCols,
		id, in.Title, in.Description, in.ImageURL, in.URL, in.Status, in.Ordem))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return item, nil
}

func (s *Server) deleteLinkShowcaseItem(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM link_showcase_items WHERE id=$1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLinkShowcaseItemNotFound
	}
	return nil
}
