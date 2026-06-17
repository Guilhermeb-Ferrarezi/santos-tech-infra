package main

import (
	"fmt"
	"net/http"
	"strings"
)

var errSocialPostNotFound = appErr(http.StatusNotFound, "SOCIAL_POST_NOT_FOUND", "Post não encontrado")

func socialPostIDFrom(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		return "", errSocialPostNotFound
	}
	return id, nil
}

func validateSocialPostInput(in *SocialPostInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Título obrigatório")
	}
	if !validSocialPlatforms[in.Platform] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Plataforma inválida")
	}
	if !validSocialPilares[in.Pilar] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Pilar inválido")
	}
	if !validSocialStatuses[in.Status] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Status inválido")
	}
	return nil
}

// GET /social/posts
func (s *Server) handleListSocialPosts(w http.ResponseWriter, r *http.Request) {
	posts, err := s.listSocialPosts(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"posts": posts})
}

// GET /social/posts/{id}
func (s *Server) handleGetSocialPost(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	post, err := s.getSocialPost(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if post == nil {
		writeErr(w, errSocialPostNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"post": post})
}

// POST /social/posts
func (s *Server) handleCreateSocialPost(w http.ResponseWriter, r *http.Request) {
	var in SocialPostInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateSocialPostInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	post, err := s.insertSocialPost(r.Context(), in, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"post": post})
}

// PUT /social/posts/{id}
func (s *Server) handleUpdateSocialPost(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in SocialPostInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateSocialPostInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	post, err := s.updateSocialPost(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if post == nil {
		writeErr(w, errSocialPostNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"post": post})
}

// DELETE /social/posts/{id}
func (s *Server) handleDeleteSocialPost(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.deleteSocialPost(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PATCH /social/posts/{id}/status
func (s *Server) handleUpdateSocialPostStatus(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if !validSocialStatuses[in.Status] {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Status inválido"))
		return
	}

	current, err := s.getSocialPost(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if current == nil {
		writeErr(w, errSocialPostNotFound)
		return
	}
	oldStatus := current.Status

	post, err := s.updateSocialPostStatus(r.Context(), id, in.Status)
	if err != nil {
		writeErr(w, err)
		return
	}
	if post == nil {
		writeErr(w, errSocialPostNotFound)
		return
	}

	changedBy := userIDFrom(r)
	if oldStatus != in.Status {
		_ = s.insertSocialPostStatusHistory(r.Context(), id, changedBy, oldStatus, in.Status)
	}

	if in.Status == "revisao" && s.cfg.SocialAlertEmail != "" {
		go func() {
			html := fmt.Sprintf(`<p>Um post foi movido para <strong>Revisão</strong> e aguarda sua aprovação.</p>
<table style="border-collapse:collapse;font-family:sans-serif;font-size:14px">
<tr><td style="padding:4px 12px 4px 0;color:#666">Título</td><td><strong>%s</strong></td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#666">Plataforma</td><td>%s</td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#666">Pilar</td><td>%s</td></tr>
</table>
<p style="margin-top:16px">Acesse o <a href="https://santos-tech.com/dashboard/social/calendario">Calendário Editorial</a> para revisar e aprovar.</p>`,
				post.Title, post.Platform, post.Pilar)
			_ = s.email.send(r.Context(), s.cfg.SocialAlertEmail,
				"Santos Tech — Post para revisão: "+post.Title, html)
		}()
	}

	writeJSON(w, http.StatusOK, map[string]any{"post": post})
}

// GET /social/posts/{id}/history
func (s *Server) handleListSocialPostStatusHistory(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	history, err := s.listSocialPostStatusHistory(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": history})
}

// GET /social/posts/{id}/notes
func (s *Server) handleListSocialPostNotes(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	notes, err := s.listSocialPostNotes(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes})
}

// POST /social/posts/{id}/notes
func (s *Server) handleAddSocialPostNote(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Conteúdo obrigatório"))
		return
	}
	note, err := s.insertSocialPostNote(r.Context(), id, userIDFrom(r), in.Content)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"note": note})
}
