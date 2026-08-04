package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type SocialPost struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Caption      string     `json:"caption"`
	Platform     string     `json:"platform"`
	Pilar        string     `json:"pilar"`
	Status       string     `json:"status"`
	ScheduledAt  *time.Time `json:"scheduledAt"`
	MediaURL     string     `json:"mediaUrl"`
	ReferenceURL string     `json:"referenceUrl"`

	Formato            string          `json:"formato"`
	Objetivo           string          `json:"objetivo"`
	Programa           string          `json:"programa"`
	Receita            string          `json:"receita"`
	PlataformasDestino []string        `json:"plataformasDestino"`
	CopyArte           json.RawMessage `json:"copyArte"`
	Hashtags           []string        `json:"hashtags"`
	ConceitoVisual     string          `json:"conceitoVisual"`
	Paleta             json.RawMessage `json:"paleta"`
	PromptIA           string          `json:"promptIa"`
	Specs              json.RawMessage `json:"specs"`
	MasterURL          string          `json:"masterUrl"`
	Mandatorios        string          `json:"mandatorios"`
	ResponsavelID      *int64          `json:"responsavelId"`

	ResponsavelNome string    `json:"responsavelNome"`
	CreatedBy       *int64    `json:"createdBy"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type SocialPostNote struct {
	ID         int64     `json:"id"`
	PostID     string    `json:"postId"`
	AuthorID   *int64    `json:"authorId"`
	AuthorName string    `json:"authorName"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"createdAt"`
}

type SocialPostInput struct {
	Title        string     `json:"title"`
	Caption      string     `json:"caption"`
	Platform     string     `json:"platform"`
	Pilar        string     `json:"pilar"`
	Status       string     `json:"status"`
	ScheduledAt  *time.Time `json:"scheduledAt"`
	MediaURL     string     `json:"mediaUrl"`
	ReferenceURL string     `json:"referenceUrl"`

	Formato            string          `json:"formato"`
	Objetivo           string          `json:"objetivo"`
	Programa           string          `json:"programa"`
	Receita            string          `json:"receita"`
	PlataformasDestino []string        `json:"plataformasDestino"`
	CopyArte           json.RawMessage `json:"copyArte"`
	Hashtags           []string        `json:"hashtags"`
	ConceitoVisual     string          `json:"conceitoVisual"`
	Paleta             json.RawMessage `json:"paleta"`
	PromptIA           string          `json:"promptIa"`
	Specs              json.RawMessage `json:"specs"`
	MasterURL          string          `json:"masterUrl"`
	Mandatorios        string          `json:"mandatorios"`
	ResponsavelID      *int64          `json:"responsavelId"`
}

var validSocialPlatforms = map[string]bool{
	"facebook": true, "instagram": true, "tiktok": true, "youtube": true,
	"threads": true, "google_meu_negocio": true, "blog": true, "twitter_x": true, "linkedin": true,
}

var validSocialPilares = map[string]bool{
	"educacional": true, "institucional": true, "captacao": true,
	"prova_social": true, "bastidores": true, "tech_mundo_real": true,
}

var validSocialStatuses = map[string]bool{
	"ideia": true, "planejado": true, "em_producao": true,
	"revisao": true, "aprovado": true, "agendado": true, "publicado": true, "arquivado": true,
}

var validSocialFormatos = map[string]bool{
	"estatico": true, "carrossel": true, "reel": true, "story": true,
	"video_longo": true, "short": true, "thumbnail": true, "card_link": true,
}
var validSocialObjetivos = map[string]bool{
	"alcance": true, "engajamento": true, "conversao": true, "autoridade": true,
}
var validSocialProgramas = map[string]bool{
	"": true, "create": true, "jr": true, "camps": true, "academies": true,
}
var validSocialReceitas = map[string]bool{
	"": true, "capa_gancho": true, "hero_numero": true, "versus": true,
	"antes_depois": true, "desenvolvimento": true, "cta_fechamento": true,
	"checklist": true, "passo_a_passo": true, "citacao_depoimento": true, "poster_anuncio": true,
}

const socialPostCols = `id::text, title, caption, platform, pilar, status,
	scheduled_at, media_url, reference_url,
	formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
	conceito_visual, paleta, prompt_ia, specs, master_url, mandatorios,
	responsavel_id, COALESCE((SELECT name FROM users WHERE id = responsavel_id), ''),
	created_by, created_at, updated_at`

func scanSocialPost(row pgx.Row) (*SocialPost, error) {
	var p SocialPost
	err := row.Scan(&p.ID, &p.Title, &p.Caption, &p.Platform, &p.Pilar, &p.Status,
		&p.ScheduledAt, &p.MediaURL, &p.ReferenceURL,
		&p.Formato, &p.Objetivo, &p.Programa, &p.Receita, &p.PlataformasDestino, &p.CopyArte, &p.Hashtags,
		&p.ConceitoVisual, &p.Paleta, &p.PromptIA, &p.Specs, &p.MasterURL, &p.Mandatorios,
		&p.ResponsavelID, &p.ResponsavelNome,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

func jsonbOrDefault(raw json.RawMessage, def string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(def)
	}
	return raw
}
func sliceOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) listSocialPosts(ctx context.Context) ([]SocialPost, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+socialPostCols+` FROM social_posts ORDER BY COALESCE(scheduled_at, created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialPost{}
	for rows.Next() {
		p, err := scanSocialPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Server) getSocialPost(ctx context.Context, id string) (*SocialPost, error) {
	return scanSocialPost(s.db.QueryRow(ctx,
		`SELECT `+socialPostCols+` FROM social_posts WHERE id = $1::uuid`, id))
}

func (s *Server) insertSocialPost(ctx context.Context, in SocialPostInput, createdBy int64) (*SocialPost, error) {
	post, err := scanSocialPost(s.db.QueryRow(ctx, `
		INSERT INTO social_posts (title, caption, platform, pilar, status, scheduled_at, media_url, reference_url,
			formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
			conceito_visual, paleta, prompt_ia, specs, master_url, mandatorios, responsavel_id, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
		RETURNING `+socialPostCols,
		in.Title, in.Caption, in.Platform, in.Pilar, in.Status, in.ScheduledAt, in.MediaURL, in.ReferenceURL,
		in.Formato, in.Objetivo, in.Programa, in.Receita, sliceOrEmpty(in.PlataformasDestino),
		jsonbOrDefault(in.CopyArte, "[]"), sliceOrEmpty(in.Hashtags),
		in.ConceitoVisual, jsonbOrDefault(in.Paleta, "{}"), in.PromptIA, jsonbOrDefault(in.Specs, "{}"),
		in.MasterURL, in.Mandatorios, in.ResponsavelID, createdBy))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return post, nil
}

func (s *Server) updateSocialPost(ctx context.Context, id string, in SocialPostInput) (*SocialPost, error) {
	post, err := scanSocialPost(s.db.QueryRow(ctx, `
		UPDATE social_posts SET
			title=$2, caption=$3, platform=$4, pilar=$5, status=$6, scheduled_at=$7, media_url=$8, reference_url=$9,
			formato=$10, objetivo=$11, programa=$12, receita=$13, plataformas_destino=$14, copy_arte=$15, hashtags=$16,
			conceito_visual=$17, paleta=$18, prompt_ia=$19, specs=$20, master_url=$21, mandatorios=$22, responsavel_id=$23, updated_at=now()
		WHERE id=$1::uuid
		RETURNING `+socialPostCols,
		id, in.Title, in.Caption, in.Platform, in.Pilar, in.Status, in.ScheduledAt, in.MediaURL, in.ReferenceURL,
		in.Formato, in.Objetivo, in.Programa, in.Receita, sliceOrEmpty(in.PlataformasDestino),
		jsonbOrDefault(in.CopyArte, "[]"), sliceOrEmpty(in.Hashtags),
		in.ConceitoVisual, jsonbOrDefault(in.Paleta, "{}"), in.PromptIA, jsonbOrDefault(in.Specs, "{}"),
		in.MasterURL, in.Mandatorios, in.ResponsavelID))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return post, nil
}

func (s *Server) updateSocialPostStatus(ctx context.Context, id, status string) (*SocialPost, error) {
	return scanSocialPost(s.db.QueryRow(ctx, `
		UPDATE social_posts SET status=$2, updated_at=now()
		WHERE id=$1::uuid RETURNING `+socialPostCols, id, status))
}

func (s *Server) deleteSocialPost(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM social_posts WHERE id=$1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errSocialPostNotFound
	}
	return nil
}

func (s *Server) listSocialPostNotes(ctx context.Context, postID string) ([]SocialPostNote, error) {
	rows, err := s.db.Query(ctx, `
		SELECT n.id, n.post_id::text, n.author_id, COALESCE(u.name,''), n.content, n.created_at
		FROM social_post_notes n
		LEFT JOIN users u ON u.id = n.author_id
		WHERE n.post_id = $1::uuid ORDER BY n.created_at`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialPostNote{}
	for rows.Next() {
		var n SocialPostNote
		if err := rows.Scan(&n.ID, &n.PostID, &n.AuthorID, &n.AuthorName, &n.Content, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type SocialPostStatusHistory struct {
	ID        int64     `json:"id"`
	PostID    string    `json:"postId"`
	ChangedBy *int64    `json:"changedById"`
	Author    string    `json:"changedBy"`
	OldStatus string    `json:"oldStatus"`
	NewStatus string    `json:"newStatus"`
	ChangedAt time.Time `json:"changedAt"`
}

func (s *Server) listSocialPostStatusHistory(ctx context.Context, postID string) ([]SocialPostStatusHistory, error) {
	rows, err := s.db.Query(ctx, `
		SELECT h.id, h.post_id::text, h.changed_by, COALESCE(u.name,''), h.old_status, h.new_status, h.changed_at
		FROM social_post_status_history h
		LEFT JOIN users u ON u.id = h.changed_by
		WHERE h.post_id = $1::uuid ORDER BY h.changed_at DESC`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialPostStatusHistory{}
	for rows.Next() {
		var h SocialPostStatusHistory
		if err := rows.Scan(&h.ID, &h.PostID, &h.ChangedBy, &h.Author, &h.OldStatus, &h.NewStatus, &h.ChangedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Server) insertSocialPostStatusHistory(ctx context.Context, postID string, changedBy int64, oldStatus, newStatus string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO social_post_status_history (post_id, changed_by, old_status, new_status)
		VALUES ($1::uuid, $2, $3, $4)`, postID, changedBy, oldStatus, newStatus)
	return err
}

func (s *Server) insertSocialPostNote(ctx context.Context, postID string, authorID int64, content string) (*SocialPostNote, error) {
	var n SocialPostNote
	err := s.db.QueryRow(ctx, `
		INSERT INTO social_post_notes (post_id, author_id, content)
		VALUES ($1::uuid, $2, $3)
		RETURNING id, post_id::text, author_id,
		          (SELECT COALESCE(name,'') FROM users WHERE id=author_id),
		          content, created_at`,
		postID, authorID, content).
		Scan(&n.ID, &n.PostID, &n.AuthorID, &n.AuthorName, &n.Content, &n.CreatedAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &n, nil
}
