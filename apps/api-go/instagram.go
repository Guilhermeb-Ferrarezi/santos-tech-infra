package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type InstagramCommentLink struct {
	MediaID   string    `json:"mediaId"`
	URL       string    `json:"url"`
	Note      string    `json:"note"`
	Keyword   string    `json:"keyword"`
	Message   string    `json:"message"`
	CreatedBy *int64    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type InstagramCommentLinkInput struct {
	URL     string `json:"url"`
	Note    string `json:"note"`
	Keyword string `json:"keyword"`
	Message string `json:"message"`
}

// instagramDMMaxLen é o teto de caracteres do texto de uma private reply na
// Graph API da Meta — validado aqui no cadastro pra falha aparecer pro admin,
// não silenciosamente no webhook.
const instagramDMMaxLen = 1000

// composeInstagramDMText monta o texto final da DM: a mensagem configurada com
// o placeholder {link} substituído pelo URL; sem placeholder, o link vai numa
// linha separada no fim; mensagem vazia = só o link.
func composeInstagramDMText(message, url string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return url
	}
	if strings.Contains(message, "{link}") {
		return strings.ReplaceAll(message, "{link}", url)
	}
	return message + "\n\n" + url
}

func validateInstagramCommentLinkInput(mediaID string, in *InstagramCommentLinkInput) error {
	if strings.TrimSpace(mediaID) == "" {
		return validationErr("media_id obrigatório")
	}
	in.URL = strings.TrimSpace(in.URL)
	parsed, err := url.ParseRequestURI(in.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return validationErr("URL de destino inválida (use http:// ou https://)")
	}
	in.Message = strings.TrimSpace(in.Message)
	if utf8.RuneCountInString(composeInstagramDMText(in.Message, in.URL)) > instagramDMMaxLen {
		return validationErr("Mensagem muito longa: com o link, o texto da DM passa de 1000 caracteres (limite da Meta)")
	}
	return nil
}

const instagramCommentLinkCols = `media_id, url, note, keyword, message, created_by, created_at, updated_at`

func scanInstagramCommentLink(row pgx.Row) (*InstagramCommentLink, error) {
	var l InstagramCommentLink
	err := row.Scan(&l.MediaID, &l.URL, &l.Note, &l.Keyword, &l.Message, &l.CreatedBy, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &l, err
}

func (s *Server) listInstagramCommentLinks(ctx context.Context) ([]InstagramCommentLink, error) {
	rows, err := s.db.Query(ctx, `SELECT `+instagramCommentLinkCols+` FROM instagram_comment_links ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstagramCommentLink{}
	for rows.Next() {
		l, err := scanInstagramCommentLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// getInstagramCommentLinkByMediaID: usada pelo receiver do webhook para decidir
// se um comentário novo tem link mapeado. nil (sem erro) = post sem mapeamento
// — o webhook ignora o evento, não é uma falha.
func (s *Server) getInstagramCommentLinkByMediaID(ctx context.Context, mediaID string) (*InstagramCommentLink, error) {
	return scanInstagramCommentLink(s.db.QueryRow(ctx,
		`SELECT `+instagramCommentLinkCols+` FROM instagram_comment_links WHERE media_id = $1`, mediaID))
}

// upsertInstagramCommentLink: PUT idempotente por media_id (chave natural, não
// UUID gerado) — repetir a mesma publicação só atualiza o link/nota.
func (s *Server) upsertInstagramCommentLink(ctx context.Context, mediaID string, in InstagramCommentLinkInput, createdBy int64) (*InstagramCommentLink, error) {
	link, err := scanInstagramCommentLink(s.db.QueryRow(ctx, `
		INSERT INTO instagram_comment_links (media_id, url, note, keyword, message, created_by)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (media_id) DO UPDATE SET url = EXCLUDED.url, note = EXCLUDED.note, keyword = EXCLUDED.keyword, message = EXCLUDED.message, updated_at = now()
		RETURNING `+instagramCommentLinkCols,
		mediaID, in.URL, in.Note, in.Keyword, in.Message, createdBy))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return link, nil
}

func (s *Server) deleteInstagramCommentLink(ctx context.Context, mediaID string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM instagram_comment_links WHERE media_id = $1`, mediaID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errInstagramCommentLinkNotFound
	}
	return nil
}
