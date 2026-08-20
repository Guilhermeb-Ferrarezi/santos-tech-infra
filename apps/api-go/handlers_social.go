package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
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
	if !validSocialFormatos[in.Formato] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Formato inválido")
	}
	if !validSocialObjetivos[in.Objetivo] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Objetivo inválido")
	}
	if !validSocialProgramas[in.Programa] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Programa inválido")
	}
	if !validSocialReceitas[in.Receita] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Receita inválida")
	}
	for _, p := range in.PlataformasDestino {
		if !validSocialPlatforms[p] {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "Plataforma-destino inválida")
		}
	}
	if !validSocialFunilEtapas[in.FunilEtapa] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Etapa de funil inválida")
	}
	// "" chegaria do front como "nenhuma pasta selecionada" — normaliza pra nil
	// antes do ::uuid do INSERT/UPDATE (string vazia não é um uuid válido).
	if in.DriveFolderID != nil && strings.TrimSpace(*in.DriveFolderID) == "" {
		in.DriveFolderID = nil
	}
	if in.DriveFolderID == nil {
		in.DriveFileID = ""
		in.DriveFileName = ""
	}
	// Mesma normalização pro trio de capa do Reel.
	if in.DriveCoverFolderID != nil && strings.TrimSpace(*in.DriveCoverFolderID) == "" {
		in.DriveCoverFolderID = nil
	}
	if in.DriveCoverFolderID == nil {
		in.DriveCoverFileID = ""
		in.DriveCoverFileName = ""
	}
	// carousel_items é livre pra salvar incompleto (planejamento em progresso) —
	// só o TETO de 10 itens no total (1 do trio principal + até 9 aqui) é uma
	// regra dura, é o limite real da Graph API do Instagram.
	if len(in.CarouselItems) > 0 {
		var items []carouselItemRef
		if err := json.Unmarshal(in.CarouselItems, &items); err != nil {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "carouselItems inválido")
		}
		if len(items) > 9 {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "Carrossel aceita no máximo 10 itens no total")
		}
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
	confirmations, err := s.listSocialPostPublishConfirmations(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	post.PublishConfirmations = confirmations
	writeJSON(w, http.StatusOK, map[string]any{"post": post})
}

// POST /social/posts
func (s *Server) handleCreateSocialPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in SocialPostInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateSocialPostInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.validateAssigneeIDs(r.Context(), in.AssigneeIDs); err != nil {
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
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in SocialPostInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateSocialPostInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.validateAssigneeIDs(r.Context(), in.AssigneeIDs); err != nil {
		writeErr(w, err)
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
	// Trava: só valida na TRANSIÇÃO pra "publicado" (post que já estava publicado
	// continua editável em outros campos mesmo sem confirmação retroativa).
	if in.Status == "publicado" && current.Status != "publicado" {
		if err := s.checkPublishConfirmationsComplete(r.Context(), id, in.PlataformasDestino); err != nil {
			writeErr(w, err)
			return
		}
	}

	post, err := s.updateSocialPost(r.Context(), id, in, userIDFrom(r))
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
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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

	// Trava: só valida na TRANSIÇÃO pra "publicado" — post que já estava
	// publicado continua podendo ser reafirmado sem exigir confirmação
	// retroativa que não existia antes desta feature.
	if in.Status == "publicado" && oldStatus != "publicado" {
		if err := s.checkPublishConfirmationsComplete(r.Context(), id, current.PlataformasDestino); err != nil {
			writeErr(w, err)
			return
		}
	}

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
		if err := s.insertSocialPostStatusHistory(r.Context(), id, changedBy, oldStatus, in.Status); err != nil {
			slog.Warn("social_post_status: falha ao gravar histórico de status", "post_id", id, "old", oldStatus, "new", in.Status, "err", err)
		}
	}

	if in.Status == "revisao" && s.cfg.SocialAlertEmail != "" {
		title, platform, pilar, to := post.Title, post.Platform, post.Pilar, s.cfg.SocialAlertEmail
		// title/platform/pilar entram no HTML do email — escapar para evitar
		// injeção de HTML (title é texto livre do usuário; os demais por defesa em profundidade).
		body := fmt.Sprintf(`<p>Um post foi movido para <strong>Revisão</strong> e aguarda sua aprovação.</p>
<table style="border-collapse:collapse;font-family:sans-serif;font-size:14px">
<tr><td style="padding:4px 12px 4px 0;color:#666">Título</td><td><strong>%s</strong></td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#666">Plataforma</td><td>%s</td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#666">Pilar</td><td>%s</td></tr>
</table>
<p style="margin-top:16px">Acesse o <a href="https://santos-tech.com/dashboard/social/calendario">Calendário Editorial</a> para revisar e aprovar.</p>`,
			html.EscapeString(title), html.EscapeString(platform), html.EscapeString(pilar))
		// remove CR/LF do título para impedir injeção de cabeçalho no assunto.
		safeTitle := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' {
				return -1
			}
			return r
		}, title)
		s.enqueueEmail("socialRevisionAlert", to, "Santos Tech — Post para revisão: "+safeTitle, body)
	}

	writeJSON(w, http.StatusOK, map[string]any{"post": post})
}

// POST /social/posts/{id}/publish — dispara a publicação automática em toda
// plataforma de destino que já tem adaptador plugado (ver social_publish.go);
// as demais continuam exigindo confirmação manual no checklist, como hoje.
// Corpo opcional `{platforms: [...]}` restringe a publicação a um SUBCONJUNTO
// de plataformasDestino nesta chamada (ex.: diálogo de revisão deixando de
// fora uma plataforma já confirmada, pra não duplicar a publicação lá) — sem
// corpo/vazio, publica em todas de plataformasDestino (comportamento original).
func (s *Server) handlePublishSocialPost(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Platforms []string `json:"platforms"`
	}
	if err := decodeJSON(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	results, err := s.PublishSocialPost(r.Context(), id, userIDFrom(r), in.Platforms)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
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
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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

// POST /social/posts/{id}/publish-confirmations/{platform}
func (s *Server) handleConfirmSocialPostPlatform(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	platform := r.PathValue("platform")
	if !validSocialPlatforms[platform] {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Plataforma inválida"))
		return
	}
	owner, err := s.getSocialPlatformOwner(r.Context(), platform)
	if err != nil {
		writeErr(w, err)
		return
	}
	if owner != nil && owner.UserID != userIDFrom(r) {
		writeErr(w, appErr(http.StatusForbidden, "NOT_PLATFORM_OWNER",
			fmt.Sprintf("Só %s pode confirmar esta plataforma.", owner.UserName)))
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
	if err := s.upsertSocialPostPublishConfirmation(r.Context(), id, platform, userIDFrom(r)); err != nil {
		writeErr(w, err)
		return
	}
	confirmations, err := s.listSocialPostPublishConfirmations(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"confirmations": confirmations})
}

// DELETE /social/posts/{id}/publish-confirmations/{platform}
func (s *Server) handleUnconfirmSocialPostPlatform(w http.ResponseWriter, r *http.Request) {
	id, err := socialPostIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	platform := r.PathValue("platform")
	if !validSocialPlatforms[platform] {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Plataforma inválida"))
		return
	}
	owner, err := s.getSocialPlatformOwner(r.Context(), platform)
	if err != nil {
		writeErr(w, err)
		return
	}
	if owner != nil && owner.UserID != userIDFrom(r) {
		writeErr(w, appErr(http.StatusForbidden, "NOT_PLATFORM_OWNER",
			fmt.Sprintf("Só %s pode confirmar esta plataforma.", owner.UserName)))
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
	if err := s.deleteSocialPostPublishConfirmation(r.Context(), id, platform); err != nil {
		writeErr(w, err)
		return
	}
	confirmations, err := s.listSocialPostPublishConfirmations(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"confirmations": confirmations})
}

// GET /social/platform-owners
func (s *Server) handleListSocialPlatformOwners(w http.ResponseWriter, r *http.Request) {
	owners, err := s.listSocialPlatformOwners(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": owners})
}

// PUT /social/platform-owners/{platform}
func (s *Server) handleSetSocialPlatformOwner(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	if !validSocialPlatforms[platform] {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Plataforma inválida"))
		return
	}
	var in struct {
		UserID int64 `json:"userId"`
	}
	if err := decodeJSON(r, &in); err != nil || in.UserID <= 0 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	target, err := s.cachedUserByID(r.Context(), in.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if target == nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Usuário não encontrado"))
		return
	}
	if err := s.setSocialPlatformOwner(r.Context(), platform, in.UserID, userIDFrom(r)); err != nil {
		writeErr(w, err)
		return
	}
	owners, err := s.listSocialPlatformOwners(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": owners})
}

// DELETE /social/platform-owners/{platform}
func (s *Server) handleDeleteSocialPlatformOwner(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	if !validSocialPlatforms[platform] {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Plataforma inválida"))
		return
	}
	if err := s.deleteSocialPlatformOwner(r.Context(), platform); err != nil {
		writeErr(w, err)
		return
	}
	owners, err := s.listSocialPlatformOwners(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": owners})
}

// GET /social/settings
func (s *Server) handleGetSocialSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.getSocialSettings(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

// PUT /social/settings — configura a localização automática marcada em toda
// publicação (ver social_publish.go). Ambos os campos são opcionais (string
// vazia = não marca local naquela rede); só o formato/teto de tamanho é
// validado aqui — não dá pra confirmar que o ID é válido de verdade sem
// chamar a Graph API, isso só se descobre na hora de publicar.
func (s *Server) handleUpdateSocialSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		InstagramLocationID string `json:"instagramLocationId"`
		FacebookPlaceID     string `json:"facebookPlaceId"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.InstagramLocationID = strings.TrimSpace(in.InstagramLocationID)
	in.FacebookPlaceID = strings.TrimSpace(in.FacebookPlaceID)
	if len(in.InstagramLocationID) > 100 || len(in.FacebookPlaceID) > 100 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "ID grande demais (máx 100 caracteres)"))
		return
	}
	settings, err := s.updateSocialSettings(r.Context(), in.InstagramLocationID, in.FacebookPlaceID, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}
