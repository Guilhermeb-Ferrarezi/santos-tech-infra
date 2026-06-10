package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5"
)

// ── Notificações por usuário (autosserviço) ──────────────────────────────────

func (s *Server) handlePortalMyNotifications(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	status := r.URL.Query().Get("status")
	if status != "read" && status != "unread" {
		status = "all"
	}
	p := portalPaginationFrom(r)
	items, total, unread, err := s.portalListMyNotifications(r.Context(), userID, status, p.Limit, p.Offset)
	if err != nil {
		writeErr(w, err)
		return
	}
	totalPages := int(math.Ceil(float64(total) / float64(p.Limit)))
	if totalPages < 1 {
		totalPages = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"unreadCount": unread,
		"total":       total,
		"pagination":  map[string]any{"page": p.Page, "limit": p.Limit, "total": total, "totalPages": totalPages},
	})
}

func (s *Server) handlePortalMarkAllRead(w http.ResponseWriter, r *http.Request) {
	n, err := s.portalMarkAllNotificationsRead(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updatedCount": n})
}

func (s *Server) handlePortalMarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "notificationId")
	if err != nil {
		writeErr(w, err)
		return
	}
	n, err := s.portalMarkNotificationRead(r.Context(), id, userIDFrom(r))
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Notificação"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notification": n})
}

// ── Templates (proxy ao gateway) ─────────────────────────────────────────────

type portalTemplateInput struct {
	Nome             string `json:"nome"`
	TituloTemplate   string `json:"tituloTemplate"`
	MensagemTemplate string `json:"mensagemTemplate"`
	Ativo            bool   `json:"ativo"`
}

func (in *portalTemplateInput) validate() error {
	if in.Nome == "" {
		return validationErr("nome interno obrigatório")
	}
	if in.TituloTemplate == "" {
		return validationErr("título obrigatório")
	}
	if in.MensagemTemplate == "" {
		return validationErr("mensagem obrigatória")
	}
	return nil
}

func (in portalTemplateInput) gatewayBody(actor map[string]any) map[string]any {
	return map[string]any{
		"name":            in.Nome,
		"titleTemplate":   in.TituloTemplate,
		"messageTemplate": in.MensagemTemplate,
		"isActive":        in.Ativo,
		"actor":           actor,
	}
}

func (s *Server) handlePortalListTemplates(w http.ResponseWriter, r *http.Request) {
	resp, err := s.callGateway(r.Context(), http.MethodGet, "/api/Notification/Admin/Templates", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !resp.Success {
		writeErr(w, appErr(http.StatusBadGateway, "INTERNAL_ERROR", resp.firstError("erro ao listar templates")))
		return
	}
	var raw []gatewayTemplate
	_ = json.Unmarshal(resp.Result, &raw)
	items := make([]portalNotifTemplateDTO, 0, len(raw))
	for _, t := range raw {
		items = append(items, t.toDTO())
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handlePortalCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var in portalTemplateInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	resp, err := s.callGateway(r.Context(), http.MethodPost, "/api/Notification/Admin/Templates", in.gatewayBody(s.portalActor(r)))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !resp.Success || len(resp.Result) == 0 {
		writeErr(w, validationErr(resp.firstError("erro ao criar template")))
		return
	}
	var t gatewayTemplate
	_ = json.Unmarshal(resp.Result, &t)
	dto := t.toDTO()
	s.portalLogActivity(r, "notification_template_create", "notification_template", fmt.Sprint(dto.ID), nil)
	writeJSON(w, http.StatusCreated, map[string]any{"template": dto})
}

func (s *Server) handlePortalUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "templateId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalTemplateInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	resp, err := s.callGateway(r.Context(), http.MethodPut, fmt.Sprintf("/api/Notification/Admin/Templates/%d", id), in.gatewayBody(s.portalActor(r)))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !resp.Success || len(resp.Result) == 0 {
		writeErr(w, validationErr(resp.firstError("erro ao atualizar template")))
		return
	}
	var t gatewayTemplate
	_ = json.Unmarshal(resp.Result, &t)
	dto := t.toDTO()
	s.portalLogActivity(r, "notification_template_update", "notification_template", fmt.Sprint(id), nil)
	writeJSON(w, http.StatusOK, map[string]any{"template": dto})
}

func (s *Server) handlePortalDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "templateId")
	if err != nil {
		writeErr(w, err)
		return
	}
	resp, err := s.callGateway(r.Context(), http.MethodDelete, fmt.Sprintf("/api/Notification/Admin/Templates/%d", id), map[string]any{"actor": s.portalActor(r)})
	if err != nil {
		writeErr(w, err)
		return
	}
	if !resp.Success {
		writeErr(w, validationErr(resp.firstError("erro ao excluir template")))
		return
	}
	s.portalLogActivity(r, "notification_template_delete", "notification_template", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Dispatches (proxy ao gateway) ────────────────────────────────────────────

func (s *Server) handlePortalListDispatches(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	path := fmt.Sprintf("/api/Notification/Admin/Dispatches?limit=%d&offset=%d", p.Limit, p.Offset)
	if p.Query != "" {
		path += "&q=" + url.QueryEscape(p.Query)
	}
	resp, err := s.callGateway(r.Context(), http.MethodGet, path, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !resp.Success {
		writeErr(w, appErr(http.StatusBadGateway, "INTERNAL_ERROR", resp.firstError("erro ao listar disparos")))
		return
	}
	var raw []gatewayDispatch
	_ = json.Unmarshal(resp.Result, &raw)
	items := make([]portalNotifDispatchDTO, 0, len(raw))
	for _, d := range raw {
		items = append(items, d.toDTO())
	}
	total := int64(len(items))
	if resp.TotalRows != nil {
		total = *resp.TotalRows
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) handlePortalDeleteDispatch(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "dispatchId")
	if err != nil {
		writeErr(w, err)
		return
	}
	resp, err := s.callGateway(r.Context(), http.MethodDelete, fmt.Sprintf("/api/Notification/Admin/Dispatches/%d", id), map[string]any{"actor": s.portalActor(r)})
	if err != nil {
		writeErr(w, err)
		return
	}
	if !resp.Success {
		writeErr(w, validationErr(resp.firstError("erro ao excluir disparo")))
		return
	}
	s.portalLogActivity(r, "notification_dispatch_delete", "notification_dispatch", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

type portalDispatchInput struct {
	CursoIDs []int64 `json:"cursoIds"`
	TurmaIDs []int64 `json:"turmaIds"`
	AlunoIDs []int64 `json:"alunoIds"`
}

// handlePortalDispatchTemplate dispara um template. Se o template usa
// placeholders de curso/turma, resolve e filtra os destinatários elegíveis
// (mesmo Postgres) antes de pedir o envio ao gateway.
func (s *Server) handlePortalDispatchTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "templateId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalDispatchInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}

	// carrega o template para inspecionar placeholders
	tplResp, err := s.callGateway(r.Context(), http.MethodGet, "/api/Notification/Admin/Templates", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !tplResp.Success {
		writeErr(w, appErr(http.StatusBadGateway, "INTERNAL_ERROR", tplResp.firstError("erro ao carregar template")))
		return
	}
	var templates []gatewayTemplate
	_ = json.Unmarshal(tplResp.Result, &templates)
	var tpl *portalNotifTemplateDTO
	for _, t := range templates {
		dto := t.toDTO()
		if dto.ID == id {
			tpl = &dto
			break
		}
	}
	if tpl == nil {
		writeErr(w, notFoundErr("Template"))
		return
	}

	reqs := portalTemplateRequirements(tpl.TituloTemplate, tpl.MensagemTemplate)
	needsContext := reqs.RequiresCurso || reqs.RequiresTurma

	body := map[string]any{"actor": s.portalActor(r)}
	if needsContext {
		recipients, err := s.portalResolveRecipients(r.Context(), in.CursoIDs, in.TurmaIDs, in.AlunoIDs)
		if err != nil {
			writeErr(w, err)
			return
		}
		eligible, err := s.portalFilterRecipientsByContext(r.Context(), recipients, reqs)
		if err != nil {
			writeErr(w, err)
			return
		}
		if len(eligible) == 0 {
			writeErr(w, validationErr("nenhum destinatário elegível para os placeholders usados no template"))
			return
		}
		body["filters"] = map[string]any{"courseIds": []int64{}, "classIds": []int64{}, "studentIds": eligible}
	} else {
		body["filters"] = map[string]any{
			"courseIds":  nonNilIDs(in.CursoIDs),
			"classIds":   nonNilIDs(in.TurmaIDs),
			"studentIds": nonNilIDs(in.AlunoIDs),
		}
	}

	resp, err := s.callGateway(r.Context(), http.MethodPost, fmt.Sprintf("/api/Notification/Admin/Templates/%d/Dispatch", id), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !resp.Success || len(resp.Result) == 0 {
		writeErr(w, validationErr(resp.firstError("erro ao disparar notificação")))
		return
	}
	s.portalLogActivity(r, "notification_dispatch_create", "notification_dispatch", fmt.Sprint(id), nil)
	// devolve o Result cru do gateway sob "dispatch" (shape do upstream)
	var dispatch map[string]any
	_ = json.Unmarshal(resp.Result, &dispatch)
	writeJSON(w, http.StatusOK, map[string]any{"dispatch": dispatch})
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// portalActor monta o ator (id/nome/email) enviado ao gateway para auditoria.
func (s *Server) portalActor(r *http.Request) map[string]any {
	uid := userIDFrom(r)
	actor := map[string]any{"externalId": fmt.Sprint(uid)}
	if u, err := s.userByID(r.Context(), uid); err == nil && u != nil {
		actor["name"] = u.Name
		actor["email"] = u.Email
	}
	return actor
}

// ── Activity logs ────────────────────────────────────────────────────────────

func (s *Server) handlePortalActivityLogs(w http.ResponseWriter, r *http.Request) {
	f := portalActivityFiltersFrom(r)
	items, total, err := s.portalListActivityLogs(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}
