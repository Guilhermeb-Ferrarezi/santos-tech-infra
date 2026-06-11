package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ── Gravação (auditoria das mutações do portal) ──────────────────────────────

// portalLogActivity grava uma linha em `logs` descrevendo uma ação admin/professor.
// É best-effort: uma falha de gravação é logada mas NUNCA propaga para o handler
// (a ação principal já aconteceu). O ator é resolvido a partir do userID da
// sessão; nome/email/role enriquecem o payload para a tela de auditoria.
//
// Não registramos request/response body aqui — só metadados estruturados que o
// handler controla — então não há risco de vazar segredo.
func (s *Server) portalLogActivity(r *http.Request, action, entityType, entityID string, metadata map[string]any) {
	ctx := r.Context()
	actorID := userIDFrom(r)
	if actorID <= 0 {
		return // sem ator (sempre há um pós-guard); logs.user_id é NOT NULL
	}

	payload := map[string]any{}
	actor := map[string]any{"id": fmt.Sprint(actorID)}
	if u, err := s.userByID(ctx, actorID); err == nil && u != nil {
		actor["name"] = u.Name
		actor["email"] = u.Email
		actor["role"] = portalRoleLabel(u.Role)
	}
	payload["actor"] = actor
	if entityID != "" {
		payload["entityId"] = entityID
	}
	payload["method"] = r.Method
	payload["endpoint"] = r.URL.Path
	if len(metadata) > 0 {
		payload["metadata"] = metadata
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("activity log: marshal falhou", "err", err, "action", action)
		return
	}
	msg := string(raw)
	if len(msg) > 8000 {
		msg = msg[:8000]
	}

	if _, err := s.portalDB.Exec(ctx, `INSERT INTO logs (user_id, "Message", action, entity_name, "LogDate")
		VALUES ($1,$2,$3,$4,NOW())`, actorID, msg, action, entityType); err != nil {
		slog.Warn("activity log: insert falhou", "err", err, "action", action)
	}
}

func portalRoleLabel(role int16) string {
	switch role {
	case RoleStudent:
		return "aluno"
	case RoleTeacher:
		return "professor"
	case RoleAdmin:
		return "admin"
	default:
		return ""
	}
}

// ── Leitura (GET /portal/activity-logs) ──────────────────────────────────────

type portalActivityActorDTO struct {
	ID    *string `json:"id"`
	Name  *string `json:"name"`
	Email *string `json:"email"`
	Role  *string `json:"role"`
}

type portalActivityLogDTO struct {
	ID         string                 `json:"id"`
	Actor      portalActivityActorDTO `json:"actor"`
	Action     string                 `json:"action"`
	EntityType string                 `json:"entityType"`
	EntityID   *string                `json:"entityId"`
	Method     *string                `json:"method"`
	Endpoint   *string                `json:"endpoint"`
	Metadata   map[string]any         `json:"metadata"`
	CreatedAt  time.Time              `json:"createdAt"`
}

type portalActivityFilters struct {
	Action     string
	EntityType string
	ActorID    string
	ActorGroup string // "user" | "staff"
	Query      string
	From       string
	To         string
	Limit      int
	Offset     int
}

func portalActivityFiltersFrom(r *http.Request) portalActivityFilters {
	q := r.URL.Query()
	limit := atoiMin(q.Get("limit"), 200, 1)
	if limit > 500 {
		limit = 500
	}
	offset := atoiMin(q.Get("offset"), 0, 0)
	return portalActivityFilters{
		Action:     strings.TrimSpace(q.Get("action")),
		EntityType: strings.TrimSpace(q.Get("entityType")),
		ActorID:    strings.TrimSpace(q.Get("actorId")),
		ActorGroup: strings.TrimSpace(q.Get("actorGroup")),
		Query:      strings.TrimSpace(q.Get("q")),
		From:       strings.TrimSpace(q.Get("from")),
		To:         strings.TrimSpace(q.Get("to")),
		Limit:      limit,
		Offset:     offset,
	}
}

func (f portalActivityFilters) build() (string, []any) {
	conds := []string{}
	args := []any{}
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Action != "" {
		add("l.action = $%d", f.Action)
	}
	if f.EntityType != "" {
		add("l.entity_name = $%d", f.EntityType)
	}
	if f.ActorID != "" {
		add("l.user_id::text = $%d", f.ActorID)
	}
	if f.From != "" {
		add(`l."LogDate" >= $%d`, f.From)
	}
	if f.To != "" {
		add(`l."LogDate" <= $%d`, f.To)
	}
	switch f.ActorGroup {
	case "user":
		conds = append(conds, "u.role = 1")
	case "staff":
		conds = append(conds, "u.role IN (2, 3)")
	}
	if f.Query != "" {
		args = append(args, "%"+f.Query+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf(`(u.name ILIKE $%d OR u.email ILIKE $%d OR l.entity_name ILIKE $%d OR l.action ILIKE $%d OR l."Message" ILIKE $%d)`, n, n, n, n, n))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	return where, args
}

func (s *Server) portalListActivityLogs(ctx context.Context, f portalActivityFilters) ([]portalActivityLogDTO, int64, error) {
	where, args := f.build()

	var total int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM logs l LEFT JOIN "user" u ON u.id = l.user_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.portalDB.Query(ctx, fmt.Sprintf(`SELECT l.id::text, l.user_id::text, u.role, l.action,
		COALESCE(l.entity_name,'unknown'), l."Message", l."LogDate", u.name, u.email
		FROM logs l LEFT JOIN "user" u ON u.id = l.user_id%s
		ORDER BY l."LogDate" DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []portalActivityLogDTO{}
	for rows.Next() {
		var (
			id         string
			actorID    *string
			actorRole  *int16
			action     string
			entityType string
			message    *string
			createdAt  time.Time
			actorName  *string
			actorEmail *string
		)
		if err := rows.Scan(&id, &actorID, &actorRole, &action, &entityType, &message, &createdAt, &actorName, &actorEmail); err != nil {
			return nil, 0, err
		}
		dto := portalActivityLogDTO{
			ID:         id,
			Action:     action,
			EntityType: entityType,
			CreatedAt:  createdAt,
			Actor: portalActivityActorDTO{
				ID:    actorID,
				Name:  actorName,
				Email: actorEmail,
				Role:  portalRoleLabelPtr(actorRole),
			},
		}
		portalApplyMessageMeta(&dto, message)
		items = append(items, dto)
	}
	return items, total, rows.Err()
}

func portalRoleLabelPtr(role *int16) *string {
	if role == nil {
		return nil
	}
	if label := portalRoleLabel(*role); label != "" {
		return &label
	}
	return nil
}

// portalApplyMessageMeta extrai entityId/method/endpoint/metadata/actor do
// payload JSON gravado por portalLogActivity (formato novo). Mensagens em
// formato legado (não-JSON) são ignoradas com graça.
func portalApplyMessageMeta(dto *portalActivityLogDTO, message *string) {
	if message == nil {
		return
	}
	trimmed := strings.TrimSpace(*message)
	if !strings.HasPrefix(trimmed, "{") {
		return
	}
	var parsed struct {
		Actor    *portalActivityActorRaw `json:"actor"`
		EntityID json.RawMessage         `json:"entityId"`
		Method   *string                 `json:"method"`
		Endpoint *string                 `json:"endpoint"`
		Metadata map[string]any          `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return
	}
	if parsed.EntityID != nil {
		if v := rawToString(parsed.EntityID); v != "" {
			dto.EntityID = &v
		}
	}
	dto.Method = parsed.Method
	dto.Endpoint = parsed.Endpoint
	dto.Metadata = parsed.Metadata
	if parsed.Actor != nil {
		if parsed.Actor.Name != nil {
			dto.Actor.Name = parsed.Actor.Name
		}
		if parsed.Actor.Email != nil {
			dto.Actor.Email = parsed.Actor.Email
		}
		if parsed.Actor.Role != nil && *parsed.Actor.Role != "" {
			dto.Actor.Role = parsed.Actor.Role
		}
	}
}

type portalActivityActorRaw struct {
	Name  *string `json:"name"`
	Email *string `json:"email"`
	Role  *string `json:"role"`
}

// rawToString aceita string ou número no JSON e devolve a forma textual.
func rawToString(raw json.RawMessage) string {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		return num.String()
	}
	return strings.Trim(string(raw), `"`)
}
