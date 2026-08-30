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
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
	ImageURL      *string   `json:"imageUrl"`
	URL           string    `json:"url"`
	Status        string    `json:"status"`
	Ordem         int       `json:"ordem"`
	TitleGradient bool      `json:"titleGradient"`
	CreatedBy     *int64    `json:"createdBy"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type LinkShowcaseItemInput struct {
	Title         string  `json:"title"`
	Description   string  `json:"description"`
	ImageURL      *string `json:"imageUrl"`
	URL           string  `json:"url"`
	Status        string  `json:"status"`
	Ordem         int     `json:"ordem"`
	TitleGradient bool    `json:"titleGradient"`
}

// LinkShowcasePublicItem é o que a rota pública expõe — nunca inclui status,
// createdBy ou timestamps (só o necessário pra renderizar um card). TitleGradient
// vai pro público porque é ela que decide se o site aplica o degradê no título.
type LinkShowcasePublicItem struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Description   string  `json:"description"`
	ImageURL      *string `json:"imageUrl"`
	URL           string  `json:"url"`
	Ordem         int     `json:"ordem"`
	TitleGradient bool    `json:"titleGradient"`
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
	if in.ImageURL != nil {
		trimmed := strings.TrimSpace(*in.ImageURL)
		if trimmed == "" {
			in.ImageURL = nil
		} else {
			imgParsed, err := url.ParseRequestURI(trimmed)
			if err != nil || (imgParsed.Scheme != "http" && imgParsed.Scheme != "https") {
				return validationErr("URL da imagem inválida (use http:// ou https://)")
			}
			in.ImageURL = &trimmed
		}
	}
	if !validLinkShowcaseStatuses[in.Status] {
		return validationErr("Status inválido")
	}
	return nil
}

func toPublicLinkShowcaseView(i LinkShowcaseItem) LinkShowcasePublicItem {
	return LinkShowcasePublicItem{
		ID: i.ID, Title: i.Title, Description: i.Description,
		ImageURL: i.ImageURL, URL: i.URL, Ordem: i.Ordem, TitleGradient: i.TitleGradient,
	}
}

const linkShowcaseCols = `id::text, title, description, image_url, url, status, ordem, title_gradient, created_by, created_at, updated_at`

func scanLinkShowcaseItem(row pgx.Row) (*LinkShowcaseItem, error) {
	var i LinkShowcaseItem
	err := row.Scan(&i.ID, &i.Title, &i.Description, &i.ImageURL, &i.URL,
		&i.Status, &i.Ordem, &i.TitleGradient, &i.CreatedBy, &i.CreatedAt, &i.UpdatedAt)
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
		INSERT INTO link_showcase_items (title, description, image_url, url, status, ordem, title_gradient, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING `+linkShowcaseCols,
		in.Title, in.Description, in.ImageURL, in.URL, in.Status, in.Ordem, in.TitleGradient, createdBy))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return item, nil
}

func (s *Server) updateLinkShowcaseItem(ctx context.Context, id string, in LinkShowcaseItemInput) (*LinkShowcaseItem, error) {
	item, err := scanLinkShowcaseItem(s.db.QueryRow(ctx, `
		UPDATE link_showcase_items SET
			title=$2, description=$3, image_url=$4, url=$5, status=$6, ordem=$7, title_gradient=$8, updated_at=now()
		WHERE id=$1::uuid
		RETURNING `+linkShowcaseCols,
		id, in.Title, in.Description, in.ImageURL, in.URL, in.Status, in.Ordem, in.TitleGradient))
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

// LinkShowcaseSettings guarda config de exibição da página inteira (não de um
// card) — hoje só a imagem de fundo. Linha única (id=1) em link_showcase_settings.
type LinkShowcaseSettings struct {
	BackgroundImageURL *string   `json:"backgroundImageUrl"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type LinkShowcaseSettingsInput struct {
	BackgroundImageURL *string `json:"backgroundImageUrl"`
}

func validateLinkShowcaseSettingsInput(in *LinkShowcaseSettingsInput) error {
	if in.BackgroundImageURL == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*in.BackgroundImageURL)
	if trimmed == "" {
		in.BackgroundImageURL = nil
		return nil
	}
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return validationErr("URL da imagem de fundo inválida (use http:// ou https://)")
	}
	in.BackgroundImageURL = &trimmed
	return nil
}

// getLinkShowcaseSettings: settings ainda não configuradas (linha nunca criada)
// devolve valores zero em vez de erro — a página pública trata como "sem fundo".
func (s *Server) getLinkShowcaseSettings(ctx context.Context) (*LinkShowcaseSettings, error) {
	var st LinkShowcaseSettings
	err := s.db.QueryRow(ctx, `SELECT background_image_url, updated_at FROM link_showcase_settings WHERE id = 1`).
		Scan(&st.BackgroundImageURL, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return &LinkShowcaseSettings{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Server) updateLinkShowcaseSettings(ctx context.Context, in LinkShowcaseSettingsInput) (*LinkShowcaseSettings, error) {
	var st LinkShowcaseSettings
	err := s.db.QueryRow(ctx, `
		INSERT INTO link_showcase_settings (id, background_image_url, updated_at)
		VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET background_image_url = EXCLUDED.background_image_url, updated_at = now()
		RETURNING background_image_url, updated_at`,
		in.BackgroundImageURL).Scan(&st.BackgroundImageURL, &st.UpdatedAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &st, nil
}
