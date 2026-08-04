package main

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var errBlogPostNotFound = appErr(http.StatusNotFound, "BLOG_POST_NOT_FOUND", "Post não encontrado")
var errBlogCategoryNotFound = appErr(http.StatusNotFound, "BLOG_CATEGORY_NOT_FOUND", "Categoria não encontrada")
var errBlogCategoryInUse = appErr(http.StatusConflict, "BLOG_CATEGORY_IN_USE", "Categoria está em uso por um ou mais posts")

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func blogPostIDFrom(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		return "", errBlogPostNotFound
	}
	return id, nil
}

func blogCategoryIDFrom(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		return "", errBlogCategoryNotFound
	}
	return id, nil
}

// blogListParams lê page/pageSize/category/q/status da querystring com defaults
// e teto sensatos (evita paginação absurda: pageSize máximo 50).
func blogListParams(r *http.Request) BlogListFilter {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if pageSize < 1 {
		pageSize = 12
	}
	if pageSize > 50 {
		pageSize = 50
	}
	return BlogListFilter{
		Page:     page,
		PageSize: pageSize,
		Category: strings.TrimSpace(r.URL.Query().Get("category")),
		Query:    strings.TrimSpace(r.URL.Query().Get("q")),
	}
}

func validateBlogPostInput(in *BlogPostInput) error {
	in.Title = strings.TrimSpace(in.Title)
	in.Slug = strings.TrimSpace(in.Slug)
	in.Excerpt = strings.TrimSpace(in.Excerpt)
	if in.Title == "" {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Título obrigatório")
	}
	if !slugRe.MatchString(in.Slug) {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Slug inválido — use letras minúsculas, números e hífens")
	}
	if !uuidRe.MatchString(in.CategoryID) {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Categoria obrigatória")
	}
	if in.Status == "" {
		in.Status = "draft"
	}
	if !validBlogStatuses[in.Status] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Status inválido")
	}
	return nil
}

func validateBlogCategoryInput(in *BlogCategoryInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.TrimSpace(in.Slug)
	if in.Name == "" {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Nome obrigatório")
	}
	if !slugRe.MatchString(in.Slug) {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Slug inválido — use letras minúsculas, números e hífens")
	}
	return nil
}

// isUniqueViolation reporta se err é uma violação de UNIQUE constraint do Postgres
// (código 23505) — usado pra devolver 409 CONFLICT em vez de 500 em slugs duplicados.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}

// ── Público (sem auth) ───────────────────────────────────────────────────────

// GET /public/blog/posts
func (s *Server) handleListPublicBlogPosts(w http.ResponseWriter, r *http.Request) {
	f := blogListParams(r)
	f.Status = "published"
	posts, total, err := s.listBlogPosts(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]BlogPostSummaryView, len(posts))
	for i, p := range posts {
		items[i] = toSummaryView(p)
	}
	writeJSON(w, http.StatusOK, BlogPaginated[BlogPostSummaryView]{Items: items, Page: f.Page, PageSize: f.PageSize, Total: total})
}

// GET /public/blog/posts/{slug}
func (s *Server) handleGetPublicBlogPost(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	post, err := s.getBlogPostBySlug(r.Context(), slug, "published")
	if err != nil {
		writeErr(w, err)
		return
	}
	if post == nil {
		writeErr(w, errBlogPostNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toPostView(*post))
}

// GET /public/blog/categories
func (s *Server) handleListBlogCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.listBlogCategories(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	views := make([]BlogCategoryView, len(cats))
	for i, c := range cats {
		views[i] = BlogCategoryView{Slug: c.Slug, Name: c.Name}
	}
	writeJSON(w, http.StatusOK, views)
}

// GET /blog/categories (admin — com id, pra editar/apagar)
func (s *Server) handleListBlogCategoriesAdmin(w http.ResponseWriter, r *http.Request) {
	cats, err := s.listBlogCategories(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cats)
}

// ── Admin (permGuard blog_posts / adminGuard+sudoGuard no delete) ──────────────

// GET /blog/posts
func (s *Server) handleListBlogPosts(w http.ResponseWriter, r *http.Request) {
	f := blogListParams(r)
	f.Status = strings.TrimSpace(r.URL.Query().Get("status"))
	if f.Status != "" && !validBlogStatuses[f.Status] {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Status inválido"))
		return
	}
	posts, total, err := s.listBlogPosts(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, BlogPaginated[BlogPost]{Items: posts, Page: f.Page, PageSize: f.PageSize, Total: total})
}

// GET /blog/posts/{id}
func (s *Server) handleGetBlogPost(w http.ResponseWriter, r *http.Request) {
	id, err := blogPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	post, err := s.getBlogPost(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if post == nil {
		writeErr(w, errBlogPostNotFound)
		return
	}
	writeJSON(w, http.StatusOK, post)
}

// POST /blog/posts
func (s *Server) handleCreateBlogPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB — corpo do post pode ter HTML razoavelmente longo
	var in BlogPostInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateBlogPostInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	post, err := s.insertBlogPost(r.Context(), in, userIDFrom(r))
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, appErr(http.StatusConflict, "SLUG_TAKEN", "Já existe um post com esse slug"))
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, post)
}

// PATCH /blog/posts/{id}
func (s *Server) handleUpdateBlogPost(w http.ResponseWriter, r *http.Request) {
	id, err := blogPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var in BlogPostInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateBlogPostInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	post, err := s.updateBlogPost(r.Context(), id, in)
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, appErr(http.StatusConflict, "SLUG_TAKEN", "Já existe um post com esse slug"))
			return
		}
		writeErr(w, err)
		return
	}
	if post == nil {
		writeErr(w, errBlogPostNotFound)
		return
	}
	writeJSON(w, http.StatusOK, post)
}

// DELETE /blog/posts/{id}
func (s *Server) handleDeleteBlogPost(w http.ResponseWriter, r *http.Request) {
	id, err := blogPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok, err := s.deleteBlogPost(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, errBlogPostNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /blog/categories
func (s *Server) handleCreateBlogCategory(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in BlogCategoryInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateBlogCategoryInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	cat, err := s.insertBlogCategory(r.Context(), in)
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, appErr(http.StatusConflict, "SLUG_TAKEN", "Já existe uma categoria com esse slug"))
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, cat)
}

// PATCH /blog/categories/{id}
func (s *Server) handleUpdateBlogCategory(w http.ResponseWriter, r *http.Request) {
	id, err := blogCategoryIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in BlogCategoryInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateBlogCategoryInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	cat, err := s.updateBlogCategory(r.Context(), id, in)
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, appErr(http.StatusConflict, "SLUG_TAKEN", "Já existe uma categoria com esse slug"))
			return
		}
		writeErr(w, err)
		return
	}
	if cat == nil {
		writeErr(w, errBlogCategoryNotFound)
		return
	}
	writeJSON(w, http.StatusOK, cat)
}

// DELETE /blog/categories/{id}
func (s *Server) handleDeleteBlogCategory(w http.ResponseWriter, r *http.Request) {
	id, err := blogCategoryIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok, err := s.deleteBlogCategory(r.Context(), id)
	if err != nil {
		if isForeignKeyViolation(err) {
			writeErr(w, errBlogCategoryInUse)
			return
		}
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, errBlogCategoryNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isForeignKeyViolation reporta se err é uma violação de FK do Postgres (código
// 23503) — usado pra devolver 409 CONFLICT ao apagar categoria ainda em uso.
func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23503")
}
