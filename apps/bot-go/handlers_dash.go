package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ── tipos de resposta/request ────────────────────────────────────────────────

type dashConv struct {
	ID           string     `json:"id"`
	ContactName  string     `json:"contactName"`
	Phone        string     `json:"phone"`
	State        string     `json:"state"`
	BotEnabled   bool       `json:"botEnabled"`
	LastActivity *time.Time `json:"lastActivity"`
	Preview      string     `json:"preview"`
}

type dashMessage struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"` // "in" | "out"
	Text      string    `json:"text"`
	Ts        time.Time `json:"ts"`
}

type kbEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
}

type dashConfig struct {
	BotName             string   `json:"botName"`
	BotGender           string   `json:"botGender"`
	BotEnabledByDefault bool     `json:"botEnabledByDefault"`
	BotAllowedNumbers   []string `json:"botAllowedNumbers"`
	QuietHoursStart     *string  `json:"quietHoursStart"`
	QuietHoursEnd       *string  `json:"quietHoursEnd"`
	KBContent           []kbEntry `json:"kbContent"`
}

// ── helpers ──────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func newHexID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ── GET /api/conversations ───────────────────────────────────────────────────

func (s *Server) handleDashConversations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)

	rows, err := s.pool.Query(ctx, `
		SELECT
			conv.id::text,
			COALESCE(c.display_name, ci.external_id) AS contact_name,
			ci.external_id AS phone,
			conv.state::text,
			conv.bot_enabled,
			GREATEST(conv.last_inbound_at, conv.last_outbound_at) AS last_activity,
			COALESCE(
				(SELECT im.content->>'Text'
				 FROM inbound_message im
				 WHERE im.conversation_id = conv.id
				 ORDER BY im.received_at DESC
				 LIMIT 1),
				''
			) AS preview
		FROM conversation conv
		JOIN channel_identity ci ON ci.id = conv.channel_identity_id
		JOIN contact c ON c.id = ci.contact_id
		WHERE conv.tenant_id = $1
		ORDER BY GREATEST(conv.last_inbound_at, conv.last_outbound_at) DESC NULLS LAST
		LIMIT 100
	`, tenantID)
	if err != nil {
		s.logger.Error("dash: list conversations", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	convs := []dashConv{}
	for rows.Next() {
		var c dashConv
		if err := rows.Scan(&c.ID, &c.ContactName, &c.Phone, &c.State, &c.BotEnabled, &c.LastActivity, &c.Preview); err != nil {
			s.logger.Error("dash: scan conversation", "err", err)
			continue
		}
		convs = append(convs, c)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, convs)
}

// ── GET /api/conversations/{id}/messages ────────────────────────────────────

func (s *Server) handleDashMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)
	convID := r.PathValue("id")
	if convID == "" {
		jsonErr(w, "missing id", http.StatusBadRequest)
		return
	}

	rows, err := s.pool.Query(ctx, `
		SELECT 'in' AS direction, id::text, COALESCE(content->>'Text', '') AS text, received_at AS ts
		FROM inbound_message
		WHERE tenant_id = $1 AND conversation_id = $2::uuid

		UNION ALL

		SELECT 'out', id::text, COALESCE(content->>'Text', ''), sent_at
		FROM outbound_message
		WHERE tenant_id = $1 AND conversation_id = $2::uuid AND status = 'sent'

		ORDER BY ts ASC
		LIMIT 200
	`, tenantID, convID)
	if err != nil {
		s.logger.Error("dash: list messages", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	msgs := []dashMessage{}
	for rows.Next() {
		var m dashMessage
		if err := rows.Scan(&m.Direction, &m.ID, &m.Text, &m.Ts); err != nil {
			s.logger.Error("dash: scan message", "err", err)
			continue
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, msgs)
}

// ── PATCH /api/conversations/{id} ───────────────────────────────────────────

func (s *Server) handleDashPatchConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)
	convID := r.PathValue("id")
	if convID == "" {
		jsonErr(w, "missing id", http.StatusBadRequest)
		return
	}

	var body struct {
		BotEnabled *bool `json:"botEnabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.BotEnabled == nil {
		jsonErr(w, "botEnabled required", http.StatusBadRequest)
		return
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE conversation SET bot_enabled = $1, updated_at = now()
		WHERE tenant_id = $2 AND id = $3::uuid
	`, *body.BotEnabled, tenantID, convID)
	if err != nil {
		s.logger.Error("dash: patch conversation", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, "not found", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

// ── POST /api/conversations/{id}/messages ───────────────────────────────────

func (s *Server) handleDashSendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)
	convID := r.PathValue("id")
	if convID == "" {
		jsonErr(w, "missing id", http.StatusBadRequest)
		return
	}

	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		jsonErr(w, "text required", http.StatusBadRequest)
		return
	}

	if s.sender.accessToken == "" || s.sender.phoneNumberID == "" {
		jsonErr(w, "Meta API não configurada", http.StatusServiceUnavailable)
		return
	}

	// Encontra o número do destinatário
	var phone string
	err := s.pool.QueryRow(ctx, `
		SELECT ci.external_id
		FROM conversation conv
		JOIN channel_identity ci ON ci.id = conv.channel_identity_id
		WHERE conv.tenant_id = $1 AND conv.id = $2::uuid
	`, tenantID, convID).Scan(&phone)
	if err != nil {
		jsonErr(w, "conversation not found", http.StatusNotFound)
		return
	}

	content := MessageContent{Type: "text", Text: body.Text}
	idemKey := newHexID()

	outMsg := OutboundMessage{
		TenantID:       tenantID,
		ConversationID: ConversationID(convID),
		Channel:        "whatsapp",
		To:             phone,
		Intent:         IntentFreeForm,
		Content:        content,
		IdempotencyKey: idemKey,
	}

	wamid, err := s.sender.SendMessage(ctx, outMsg)
	if err != nil {
		s.logger.Error("dash: send message", "err", err)
		jsonErr(w, "falha ao enviar mensagem", http.StatusInternalServerError)
		return
	}

	contentJSON, _ := json.Marshal(content)
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO outbound_message
			(tenant_id, conversation_id, provider_message_id, idempotency_key, intent_category, content, status, sent_at)
		VALUES ($1, $2::uuid, $3, $4, 'FREE_FORM'::outbound_intent, $5, 'sent', now())
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
	`, tenantID, convID, wamid, idemKey, contentJSON)

	jsonOK(w, map[string]string{"wamid": wamid})
}

// ── GET /api/config ──────────────────────────────────────────────────────────

func (s *Server) handleDashGetConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)

	var cfg dashConfig
	var allowedRaw []byte
	var qhStart, qhEnd *string
	var kbRaw *string

	err := s.pool.QueryRow(ctx, `
		SELECT tc.bot_name, tc.bot_gender, tc.bot_enabled_by_default,
		       tc.bot_allowed_numbers,
		       tc.quiet_hours->>'start', tc.quiet_hours->>'end',
		       tc.kb_content::text
		FROM tenant_config tc
		WHERE tc.tenant_id = $1
	`, tenantID).Scan(
		&cfg.BotName, &cfg.BotGender, &cfg.BotEnabledByDefault,
		&allowedRaw, &qhStart, &qhEnd, &kbRaw,
	)
	if err != nil {
		s.logger.Error("dash: get config", "err", err)
		jsonErr(w, "config not found", http.StatusNotFound)
		return
	}

	if len(allowedRaw) > 0 {
		_ = json.Unmarshal(allowedRaw, &cfg.BotAllowedNumbers)
	}
	if cfg.BotAllowedNumbers == nil {
		cfg.BotAllowedNumbers = []string{}
	}
	cfg.QuietHoursStart = qhStart
	cfg.QuietHoursEnd = qhEnd

	if kbRaw != nil && *kbRaw != "" && *kbRaw != "null" {
		_ = json.Unmarshal([]byte(*kbRaw), &cfg.KBContent)
	}
	if cfg.KBContent == nil {
		cfg.KBContent = []kbEntry{}
	}

	jsonOK(w, cfg)
}

// ── PATCH /api/config ────────────────────────────────────────────────────────

func (s *Server) handleDashPatchConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)

	var body dashConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, "invalid body", http.StatusBadRequest)
		return
	}

	allowedJSON, _ := json.Marshal(body.BotAllowedNumbers)
	if body.BotAllowedNumbers == nil {
		allowedJSON = []byte("[]")
	}

	kbJSON, _ := json.Marshal(body.KBContent)
	if body.KBContent == nil {
		kbJSON = []byte("[]")
	}

	var quietHoursJSON *string
	if body.QuietHoursStart != nil && body.QuietHoursEnd != nil &&
		*body.QuietHoursStart != "" && *body.QuietHoursEnd != "" {
		v := fmt.Sprintf(`{"start":%q,"end":%q}`, *body.QuietHoursStart, *body.QuietHoursEnd)
		quietHoursJSON = &v
	}

	_, err := s.pool.Exec(ctx, `
		UPDATE tenant_config
		SET bot_name             = $1,
		    bot_gender           = $2,
		    bot_enabled_by_default = $3,
		    bot_allowed_numbers  = $4::jsonb,
		    quiet_hours          = CASE WHEN $5::text IS NULL THEN '{}'::jsonb ELSE $5::jsonb END,
		    kb_content           = $6::jsonb
		WHERE tenant_id = $7
	`, body.BotName, body.BotGender, body.BotEnabledByDefault,
		allowedJSON, quietHoursJSON, kbJSON, tenantID)
	if err != nil {
		s.logger.Error("dash: patch config", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

// handleDashGetConversationDetail returns the contact info + bot_enabled for a conversation.
func (s *Server) handleDashGetConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)
	convID := r.PathValue("id")
	if convID == "" {
		jsonErr(w, "missing id", http.StatusBadRequest)
		return
	}

	var c dashConv
	err := s.pool.QueryRow(ctx, `
		SELECT
			conv.id::text,
			COALESCE(c.display_name, ci.external_id) AS contact_name,
			ci.external_id AS phone,
			conv.state::text,
			conv.bot_enabled,
			GREATEST(conv.last_inbound_at, conv.last_outbound_at) AS last_activity,
			''
		FROM conversation conv
		JOIN channel_identity ci ON ci.id = conv.channel_identity_id
		JOIN contact c ON c.id = ci.contact_id
		WHERE conv.tenant_id = $1 AND conv.id = $2::uuid
	`, tenantID, convID).Scan(
		&c.ID, &c.ContactName, &c.Phone, &c.State, &c.BotEnabled, &c.LastActivity, &c.Preview,
	)
	if err != nil {
		jsonErr(w, "not found", http.StatusNotFound)
		return
	}
	jsonOK(w, c)
}

// Garante que context é usado (lint).
var _ = context.Background
