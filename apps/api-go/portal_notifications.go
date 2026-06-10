package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Notificações por usuário (tabela `notification` no Postgres central) ──────

type portalNotificationDTO struct {
	ID           int64      `json:"id"`
	Title        string     `json:"title"`
	Message      string     `json:"message"`
	MetadataJSON *string    `json:"metadataJson"`
	ReadAt       *time.Time `json:"readAt"`
	CreatedAt    time.Time  `json:"createdAt"`
}

func (s *Server) portalListMyNotifications(ctx context.Context, userID int64, status string, limit, offset int) ([]portalNotificationDTO, int64, int64, error) {
	cond := ""
	switch status {
	case "read":
		cond = " AND read_at IS NOT NULL"
	case "unread":
		cond = " AND read_at IS NULL"
	}

	var total, unread int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM notification WHERE user_id=$1`+cond, userID).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM notification WHERE user_id=$1 AND read_at IS NULL`, userID).Scan(&unread); err != nil {
		return nil, 0, 0, err
	}

	rows, err := s.portalDB.Query(ctx, `SELECT id, COALESCE(title,''), COALESCE(message,''), metadata_json, read_at, created_at
		FROM notification WHERE user_id=$1`+cond+` ORDER BY created_at DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	items := []portalNotificationDTO{}
	for rows.Next() {
		var n portalNotificationDTO
		if err := rows.Scan(&n.ID, &n.Title, &n.Message, &n.MetadataJSON, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, 0, 0, err
		}
		items = append(items, n)
	}
	return items, total, unread, rows.Err()
}

func (s *Server) portalMarkAllNotificationsRead(ctx context.Context, userID int64) (int64, error) {
	tag, err := s.portalDB.Exec(ctx, `UPDATE notification SET read_at = NOW() WHERE user_id=$1 AND read_at IS NULL`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Server) portalMarkNotificationRead(ctx context.Context, id, userID int64) (*portalNotificationDTO, error) {
	var n portalNotificationDTO
	err := s.portalDB.QueryRow(ctx, `UPDATE notification SET read_at = COALESCE(read_at, NOW())
		WHERE id=$1 AND user_id=$2
		RETURNING id, COALESCE(title,''), COALESCE(message,''), metadata_json, read_at, created_at`,
		id, userID).Scan(&n.ID, &n.Title, &n.Message, &n.MetadataJSON, &n.ReadAt, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// ── Resolução de destinatários (mesmo Postgres) ──────────────────────────────

var templatePlaceholderRe = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

type portalTemplateContext struct {
	RequiresCurso bool
	RequiresTurma bool
}

// portalTemplateRequirements inspeciona título+mensagem do template e detecta se
// usa placeholders de curso ({{curso...}}/{{course...}}) ou turma.
func portalTemplateRequirements(title, message string) portalTemplateContext {
	var ctx portalTemplateContext
	for _, m := range templatePlaceholderRe.FindAllStringSubmatch(title+"\n"+message, -1) {
		root := strings.ToLower(strings.TrimSpace(m[1]))
		if i := strings.Index(root, "."); i >= 0 {
			root = root[:i]
		}
		switch strings.TrimSpace(root) {
		case "curso", "course":
			ctx.RequiresCurso = true
		case "turma", "class":
			ctx.RequiresTurma = true
		}
	}
	return ctx
}

// portalResolveRecipients junta alunoIds explícitos + alunos de turmas + alunos
// de cursos (via enrollment), deduplicando.
func (s *Server) portalResolveRecipients(ctx context.Context, cursoIDs, turmaIDs, alunoIDs []int64) ([]int64, error) {
	set := map[int64]struct{}{}
	for _, id := range alunoIDs {
		if id > 0 {
			set[id] = struct{}{}
		}
	}
	if len(turmaIDs) > 0 {
		rows, err := s.portalDB.Query(ctx, `SELECT DISTINCT e.user_id FROM enrollment e WHERE e.class_id = ANY($1::int[])`, turmaIDs)
		if err != nil {
			return nil, err
		}
		if err := collectIDs(rows, set); err != nil {
			return nil, err
		}
	}
	if len(cursoIDs) > 0 {
		rows, err := s.portalDB.Query(ctx, `SELECT DISTINCT e.user_id FROM enrollment e
			JOIN class c ON c.id = e.class_id WHERE c.course_id = ANY($1::int[])`, cursoIDs)
		if err != nil {
			return nil, err
		}
		if err := collectIDs(rows, set); err != nil {
			return nil, err
		}
	}
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out, nil
}

func collectIDs(rows pgx.Rows, set map[int64]struct{}) error {
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		set[id] = struct{}{}
	}
	return rows.Err()
}

// portalFilterRecipientsByContext mantém só destinatários que têm o contexto
// exigido pelo template (matrícula em turma e/ou curso).
func (s *Server) portalFilterRecipientsByContext(ctx context.Context, ids []int64, req portalTemplateContext) ([]int64, error) {
	if len(ids) == 0 || (!req.RequiresCurso && !req.RequiresTurma) {
		return ids, nil
	}
	rows, err := s.portalDB.Query(ctx, `SELECT u.id,
		EXISTS(SELECT 1 FROM enrollment e WHERE e.user_id = u.id) AS has_turma,
		EXISTS(SELECT 1 FROM enrollment e JOIN class c ON c.id = e.class_id WHERE e.user_id = u.id AND c.course_id IS NOT NULL) AS has_curso
		FROM "user" u WHERE u.id = ANY($1::int[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		var hasTurma, hasCurso bool
		if err := rows.Scan(&id, &hasTurma, &hasCurso); err != nil {
			return nil, err
		}
		if req.RequiresTurma && !hasTurma {
			continue
		}
		if req.RequiresCurso && !hasCurso {
			continue
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ── Gateway externo (templates/dispatches vivem no portal do aluno) ───────────

var notificationHTTP = &http.Client{Timeout: 15 * time.Second}

type gatewayResponse struct {
	Success   bool            `json:"-"`
	Errors    []string        `json:"-"`
	Result    json.RawMessage `json:"-"`
	TotalRows *int64          `json:"-"`
}

// gatewayConfigured indica se o gateway de notificações está configurado.
func (s *Server) gatewayConfigured() bool {
	return s.cfg.NotificationsGatewayURL != "" && s.cfg.NotificationsSharedSecret != ""
}

// callGateway faz a requisição autenticada ao gateway e normaliza a resposta
// (que vem em PascalCase ou camelCase). Erro de rede/config vira *AppError 502.
func (s *Server) callGateway(ctx context.Context, method, path string, body any) (*gatewayResponse, error) {
	if !s.gatewayConfigured() {
		return nil, appErr(http.StatusBadGateway, "INTERNAL_ERROR", "gateway de notificações não configurado")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.NotificationsGatewayURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-notification-admin-secret", s.cfg.NotificationsSharedSecret)

	res, err := notificationHTTP.Do(req)
	if err != nil {
		return nil, appErr(http.StatusBadGateway, "INTERNAL_ERROR", "falha ao conectar com o portal do aluno")
	}
	defer res.Body.Close()

	var payload struct {
		Success    *bool           `json:"Success"`
		SuccessLow *bool           `json:"success"`
		Errors     []string        `json:"Errors"`
		ErrorsLow  []string        `json:"errors"`
		Result     json.RawMessage `json:"Result"`
		ResultLow  json.RawMessage `json:"result"`
		TotalRows  *int64          `json:"TotalRows"`
		TotalLow   *int64          `json:"totalRows"`
	}
	_ = json.NewDecoder(res.Body).Decode(&payload)

	gr := &gatewayResponse{}
	gr.Success = (payload.Success != nil && *payload.Success) || (payload.SuccessLow != nil && *payload.SuccessLow)
	if len(payload.Errors) > 0 {
		gr.Errors = payload.Errors
	} else {
		gr.Errors = payload.ErrorsLow
	}
	if len(payload.Result) > 0 {
		gr.Result = payload.Result
	} else {
		gr.Result = payload.ResultLow
	}
	if payload.TotalRows != nil {
		gr.TotalRows = payload.TotalRows
	} else {
		gr.TotalRows = payload.TotalLow
	}

	if res.StatusCode >= 300 {
		gr.Success = false
		if len(gr.Errors) == 0 {
			gr.Errors = []string{fmt.Sprintf("upstream %d ao acessar %s", res.StatusCode, path)}
		}
	}
	return gr, nil
}

func (g *gatewayResponse) firstError(fallback string) string {
	if len(g.Errors) > 0 && g.Errors[0] != "" {
		return g.Errors[0]
	}
	return fallback
}

// ── DTOs do gateway (normalizados para o dashboard) ──────────────────────────

type portalNotifTemplateDTO struct {
	ID               int64  `json:"id"`
	Nome             string `json:"nome"`
	TituloTemplate   string `json:"tituloTemplate"`
	MensagemTemplate string `json:"mensagemTemplate"`
	Ativo            bool   `json:"ativo"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

// gatewayTemplate é o shape cru do gateway (aceita Pascal e camelCase).
type gatewayTemplate struct {
	ID                 *int64  `json:"Id"`
	IDLow              *int64  `json:"id"`
	Name               *string `json:"Name"`
	NameLow            *string `json:"name"`
	TitleTemplate      *string `json:"TitleTemplate"`
	TitleTemplateLow   *string `json:"titleTemplate"`
	MessageTemplate    *string `json:"MessageTemplate"`
	MessageTemplateLow *string `json:"messageTemplate"`
	IsActive           *bool   `json:"IsActive"`
	IsActiveLow        *bool   `json:"isActive"`
	CreatedAt          *string `json:"CreatedAt"`
	CreatedAtLow       *string `json:"createdAt"`
	UpdatedAt          *string `json:"UpdatedAt"`
	UpdatedAtLow       *string `json:"updatedAt"`
}

func (t gatewayTemplate) toDTO() portalNotifTemplateDTO {
	return portalNotifTemplateDTO{
		ID:               firstInt(t.ID, t.IDLow),
		Nome:             firstStr(t.Name, t.NameLow),
		TituloTemplate:   firstStr(t.TitleTemplate, t.TitleTemplateLow),
		MensagemTemplate: firstStr(t.MessageTemplate, t.MessageTemplateLow),
		Ativo:            firstBool(t.IsActive, t.IsActiveLow),
		CreatedAt:        firstStr(t.CreatedAt, t.CreatedAtLow),
		UpdatedAt:        firstStr(t.UpdatedAt, t.UpdatedAtLow),
	}
}

type portalNotifDispatchDTO struct {
	ID                    int64   `json:"id"`
	TemplateID            int64   `json:"templateId"`
	TemplateName          string  `json:"templateName"`
	TriggeredByActorName  *string `json:"triggeredByActorName"`
	TriggeredByActorEmail *string `json:"triggeredByActorEmail"`
	CursoIDs              []int64 `json:"cursoIds"`
	TurmaIDs              []int64 `json:"turmaIds"`
	AlunoIDs              []int64 `json:"alunoIds"`
	TotalRecipients       int64   `json:"totalRecipients"`
	FailedRecipients      int64   `json:"failedRecipients"`
	CreatedAt             string  `json:"createdAt"`
}

type gatewayDispatch struct {
	ID                     *int64  `json:"Id"`
	IDLow                  *int64  `json:"id"`
	NotificationTemplateID *int64  `json:"NotificationTemplateId"`
	TemplateIDLow          *int64  `json:"notificationTemplateId"`
	TemplateName           *string `json:"TemplateName"`
	TemplateNameLow        *string `json:"templateName"`
	TriggeredByActorName   *string `json:"TriggeredByActorName"`
	TriggeredByActorEmail  *string `json:"TriggeredByActorEmail"`
	Filters                *struct {
		CourseIDs  []int64 `json:"CourseIds"`
		ClassIDs   []int64 `json:"ClassIds"`
		StudentIDs []int64 `json:"StudentIds"`
	} `json:"Filters"`
	TotalRecipients  *int64  `json:"TotalRecipients"`
	FailedRecipients *int64  `json:"FailedRecipients"`
	CreatedAt        *string `json:"CreatedAt"`
	CreatedAtLow     *string `json:"createdAt"`
}

func (d gatewayDispatch) toDTO() portalNotifDispatchDTO {
	out := portalNotifDispatchDTO{
		ID:                    firstInt(d.ID, d.IDLow),
		TemplateID:            firstInt(d.NotificationTemplateID, d.TemplateIDLow),
		TemplateName:          firstStr(d.TemplateName, d.TemplateNameLow),
		TriggeredByActorName:  d.TriggeredByActorName,
		TriggeredByActorEmail: d.TriggeredByActorEmail,
		CursoIDs:              []int64{},
		TurmaIDs:              []int64{},
		AlunoIDs:              []int64{},
		TotalRecipients:       firstInt(d.TotalRecipients, nil),
		FailedRecipients:      firstInt(d.FailedRecipients, nil),
		CreatedAt:             firstStr(d.CreatedAt, d.CreatedAtLow),
	}
	if d.Filters != nil {
		if d.Filters.CourseIDs != nil {
			out.CursoIDs = d.Filters.CourseIDs
		}
		if d.Filters.ClassIDs != nil {
			out.TurmaIDs = d.Filters.ClassIDs
		}
		if d.Filters.StudentIDs != nil {
			out.AlunoIDs = d.Filters.StudentIDs
		}
	}
	return out
}

func firstStr(a, b *string) string {
	if a != nil {
		return *a
	}
	if b != nil {
		return *b
	}
	return ""
}

func firstInt(a, b *int64) int64 {
	if a != nil {
		return *a
	}
	if b != nil {
		return *b
	}
	return 0
}

func firstBool(a, b *bool) bool {
	if a != nil {
		return *a
	}
	if b != nil {
		return *b
	}
	return false
}
