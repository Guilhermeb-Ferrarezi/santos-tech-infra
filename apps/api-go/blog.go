package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Models ───────────────────────────────────────────────────────────────────

type BlogCategory struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type BlogCategoryInput struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// BlogPost é a view ADMIN (CRUD completo, todos os campos e status).
type BlogPost struct {
	ID            string     `json:"id"`
	Slug          string     `json:"slug"`
	Title         string     `json:"title"`
	Excerpt       string     `json:"excerpt"`
	ContentHTML   string     `json:"contentHtml"`
	CoverImageURL *string    `json:"coverImageUrl"`
	CategoryID    string     `json:"categoryId"`
	CategorySlug  string     `json:"categorySlug"`
	CategoryName  string     `json:"categoryName"`
	AuthorID      *int64     `json:"authorId"`
	AuthorName    string     `json:"authorName"`
	Status        string     `json:"status"`
	PublishedAt   *time.Time `json:"publishedAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type BlogPostInput struct {
	Slug          string  `json:"slug"`
	Title         string  `json:"title"`
	Excerpt       string  `json:"excerpt"`
	ContentHTML   string  `json:"contentHtml"`
	CoverImageURL *string `json:"coverImageUrl"`
	CategoryID    string  `json:"categoryId"`
	Status        string  `json:"status"`
}

var validBlogStatuses = map[string]bool{"draft": true, "published": true}

// ── Views públicas (só o que o blog público (sem auth) precisa) ────────────────

type BlogAuthorView struct {
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatarUrl,omitempty"`
}

type BlogCategoryView struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type BlogPostSummaryView struct {
	Slug          string           `json:"slug"`
	Title         string           `json:"title"`
	Excerpt       string           `json:"excerpt"`
	CoverImageURL *string          `json:"coverImageUrl,omitempty"`
	Category      BlogCategoryView `json:"category"`
	Author        BlogAuthorView   `json:"author"`
	PublishedAt   *time.Time       `json:"publishedAt"`
}

type BlogPostView struct {
	BlogPostSummaryView
	ContentHTML string `json:"contentHtml"`
}

type BlogPaginated[T any] struct {
	Items    []T `json:"items"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
	Total    int `json:"total"`
}

func toSummaryView(p BlogPost) BlogPostSummaryView {
	return BlogPostSummaryView{
		Slug:          p.Slug,
		Title:         p.Title,
		Excerpt:       p.Excerpt,
		CoverImageURL: p.CoverImageURL,
		Category:      BlogCategoryView{Slug: p.CategorySlug, Name: p.CategoryName},
		Author:        BlogAuthorView{Name: p.AuthorName},
		PublishedAt:   p.PublishedAt,
	}
}

func toPostView(p BlogPost) BlogPostView {
	return BlogPostView{BlogPostSummaryView: toSummaryView(p), ContentHTML: p.ContentHTML}
}

// ── Store — categorias ───────────────────────────────────────────────────────

const blogCategoryCols = `id::text, slug, name`

func scanBlogCategory(row pgx.Row) (*BlogCategory, error) {
	var c BlogCategory
	err := row.Scan(&c.ID, &c.Slug, &c.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

func (s *Server) listBlogCategories(ctx context.Context) ([]BlogCategory, error) {
	rows, err := s.db.Query(ctx, `SELECT `+blogCategoryCols+` FROM blog_categories ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BlogCategory{}
	for rows.Next() {
		c, err := scanBlogCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Server) getBlogCategory(ctx context.Context, id string) (*BlogCategory, error) {
	return scanBlogCategory(s.db.QueryRow(ctx, `SELECT `+blogCategoryCols+` FROM blog_categories WHERE id=$1::uuid`, id))
}

func (s *Server) insertBlogCategory(ctx context.Context, in BlogCategoryInput) (*BlogCategory, error) {
	return scanBlogCategory(s.db.QueryRow(ctx,
		`INSERT INTO blog_categories (slug, name) VALUES ($1,$2) RETURNING `+blogCategoryCols,
		in.Slug, in.Name))
}

func (s *Server) updateBlogCategory(ctx context.Context, id string, in BlogCategoryInput) (*BlogCategory, error) {
	return scanBlogCategory(s.db.QueryRow(ctx,
		`UPDATE blog_categories SET slug=$2, name=$3 WHERE id=$1::uuid RETURNING `+blogCategoryCols,
		id, in.Slug, in.Name))
}

func (s *Server) deleteBlogCategory(ctx context.Context, id string) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM blog_categories WHERE id=$1::uuid`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ── Store — posts ────────────────────────────────────────────────────────────

// blogPostCols faz JOIN com categoria e autor pra evitar N+1 na listagem.
const blogPostCols = `p.id::text, p.slug, p.title, p.excerpt, p.content_html, p.cover_image_url,
	p.category_id::text, c.slug, c.name,
	p.author_id, COALESCE(u.name, ''),
	p.status, p.published_at, p.created_at, p.updated_at`

const blogPostFrom = `FROM blog_posts p
	JOIN blog_categories c ON c.id = p.category_id
	LEFT JOIN users u ON u.id = p.author_id`

func scanBlogPost(row pgx.Row) (*BlogPost, error) {
	var p BlogPost
	err := row.Scan(&p.ID, &p.Slug, &p.Title, &p.Excerpt, &p.ContentHTML, &p.CoverImageURL,
		&p.CategoryID, &p.CategorySlug, &p.CategoryName,
		&p.AuthorID, &p.AuthorName,
		&p.Status, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

func collectBlogPosts(rows pgx.Rows) ([]BlogPost, error) {
	defer rows.Close()
	out := []BlogPost{}
	for rows.Next() {
		var p BlogPost
		if err := rows.Scan(&p.ID, &p.Slug, &p.Title, &p.Excerpt, &p.ContentHTML, &p.CoverImageURL,
			&p.CategoryID, &p.CategorySlug, &p.CategoryName,
			&p.AuthorID, &p.AuthorName,
			&p.Status, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BlogListFilter é comum às listagens pública e admin. Status vazio (só usado
// no admin) significa "qualquer status"; a listagem pública sempre força "published".
type BlogListFilter struct {
	Page     int
	PageSize int
	Category string // slug
	Query    string
	Status   string
}

func (s *Server) listBlogPosts(ctx context.Context, f BlogListFilter) ([]BlogPost, int, error) {
	where := []string{}
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.Status != "" {
		where = append(where, "p.status = "+arg(f.Status))
	}
	if f.Category != "" {
		where = append(where, "c.slug = "+arg(f.Category))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + q + "%"
		where = append(where, "(p.title ILIKE "+arg(like)+" OR p.excerpt ILIKE "+arg(like)+")")
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	countSQL := "SELECT COUNT(*) " + blogPostFrom + " " + whereSQL
	if err := s.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := arg(f.PageSize)
	offsetArg := arg((f.Page - 1) * f.PageSize)
	listSQL := "SELECT " + blogPostCols + " " + blogPostFrom + " " + whereSQL +
		" ORDER BY COALESCE(p.published_at, p.created_at) DESC LIMIT " + limitArg + " OFFSET " + offsetArg
	rows, err := s.db.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	posts, err := collectBlogPosts(rows)
	if err != nil {
		return nil, 0, err
	}
	return posts, total, nil
}

func (s *Server) getBlogPost(ctx context.Context, id string) (*BlogPost, error) {
	return scanBlogPost(s.db.QueryRow(ctx,
		`SELECT `+blogPostCols+` `+blogPostFrom+` WHERE p.id = $1::uuid`, id))
}

func (s *Server) getBlogPostBySlug(ctx context.Context, slug string, status string) (*BlogPost, error) {
	return scanBlogPost(s.db.QueryRow(ctx,
		`SELECT `+blogPostCols+` `+blogPostFrom+` WHERE p.slug = $1 AND p.status = $2`, slug, status))
}

func (s *Server) insertBlogPost(ctx context.Context, in BlogPostInput, authorID int64) (*BlogPost, error) {
	var publishedAt *time.Time
	if in.Status == "published" {
		now := time.Now()
		publishedAt = &now
	}
	// Sanitiza aqui, não no handler: nunca confiamos no HTML recebido, seja
	// ele vindo do editor do dashboard ou de qualquer outro client da API.
	contentHTML := sanitizeBlogContentHTML(in.ContentHTML)
	var id string
	err := s.db.QueryRow(ctx, `
		INSERT INTO blog_posts (slug, title, excerpt, content_html, cover_image_url, category_id, author_id, status, published_at)
		VALUES ($1,$2,$3,$4,$5,$6::uuid,$7,$8,$9)
		RETURNING id::text`,
		in.Slug, in.Title, in.Excerpt, contentHTML, in.CoverImageURL, in.CategoryID, authorID, in.Status, publishedAt).
		Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.getBlogPost(ctx, id)
}

// updateBlogPost seta published_at = now() na transição draft→published (só se
// ainda não tinha sido publicado antes); nas demais mudanças de status preserva
// o published_at histórico.
func (s *Server) updateBlogPost(ctx context.Context, id string, in BlogPostInput) (*BlogPost, error) {
	// Sanitiza aqui, não no handler: nunca confiamos no HTML recebido, seja
	// ele vindo do editor do dashboard ou de qualquer outro client da API.
	contentHTML := sanitizeBlogContentHTML(in.ContentHTML)
	_, err := s.db.Exec(ctx, `
		UPDATE blog_posts SET
			slug=$2, title=$3, excerpt=$4, content_html=$5, cover_image_url=$6, category_id=$7::uuid, status=$8,
			published_at = CASE WHEN $8 = 'published' AND published_at IS NULL THEN now() ELSE published_at END,
			updated_at = now()
		WHERE id=$1::uuid`,
		id, in.Slug, in.Title, in.Excerpt, contentHTML, in.CoverImageURL, in.CategoryID, in.Status)
	if err != nil {
		return nil, err
	}
	return s.getBlogPost(ctx, id)
}

func (s *Server) deleteBlogPost(ctx context.Context, id string) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM blog_posts WHERE id=$1::uuid`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
