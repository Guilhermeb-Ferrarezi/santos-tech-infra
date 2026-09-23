package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── tipos de resposta/request ────────────────────────────────────────────────

type dashProcessingLog struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"` // "message" | "action"
	ConversationID string          `json:"conversationId"`
	ContactPhone   string          `json:"contactPhone"`
	ContactName    string          `json:"contactName"`
	InboundText    string          `json:"inboundText"`
	Answered       *bool           `json:"answered"`
	AnsweredFromKb *bool           `json:"answeredFromKb"`
	Handoff        *bool           `json:"handoff"`
	CitedEntryIDs  []string        `json:"citedEntryIds"`
	Bubbles        []string        `json:"bubbles"`
	ToolCalls      json.RawMessage `json:"toolCalls,omitempty"`
	ProcessingMs   int             `json:"processingMs"`
	Error          string          `json:"error,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

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
	ID        string           `json:"id"`
	Direction string           `json:"direction"` // "in" | "out"
	Text      string           `json:"text"`
	Ts        time.Time        `json:"ts"`
	Reasoning *json.RawMessage `json:"reasoning,omitempty"`
}

type dashConfig struct {
	BotName                     string    `json:"botName"`
	BotGender                   string    `json:"botGender"`
	BotEnabledByDefault         bool      `json:"botEnabledByDefault"`
	BotAllowedNumbers           []string  `json:"botAllowedNumbers"`
	QuietHoursStart             *string   `json:"quietHoursStart"`
	QuietHoursEnd               *string   `json:"quietHoursEnd"`
	KBContent                   []KBEntry `json:"kbContent"`
	SystemPrompt                string    `json:"systemPrompt"`
	AdminSystemPrompt           string    `json:"adminSystemPrompt"`
	AdminWhatsAppNumbers        []string  `json:"adminWhatsAppNumbers"`
	DebounceMs                  int       `json:"debounceMs"`
	EvolutionBotReplyEnabled    bool      `json:"evolutionBotReplyEnabled"`
	EvolutionLeadCaptureEnabled bool      `json:"evolutionLeadCaptureEnabled"`
	// Captação por número: nomes das instâncias do Evolution com captação DESLIGADA
	// (ausência = captando). Substitui o toggle global na prática.
	EvolutionCaptureDisabled []string `json:"evolutionCaptureDisabled"`

	// Notificações de deploy (Coolify → WhatsApp)
	NotifPhone           string `json:"notifPhone"`
	NotifInstance        string `json:"notifInstance"`
	NotifEnabled         bool   `json:"notifEnabled"`
	NotifOnSuccess       bool   `json:"notifOnSuccess"`
	NotifOnContainerDown bool   `json:"notifOnContainerDown"`

	// Voz (0033) — qual voz o bot usa ao responder uma mensagem de áudio.
	//
	// Ponteiros de propósito: um painel que ainda não conhece estes campos não
	// os envia no PATCH, e aí `nil` significa "não mexe" em vez de "desliga".
	// Sem isso, salvar qualquer outra configuração no painel antigo apagaria a
	// escolha de voz.
	VoiceEnabled  *bool   `json:"voiceEnabled"`
	VoiceProvider *string `json:"voiceProvider"` // "openai" | "elevenlabs"
	VoiceID       *string `json:"voiceId"`
	VoiceModel    *string `json:"voiceModel"`
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

// validRescheduleSource valida a origem aceita em POST /api/bookings/reschedule.
func validRescheduleSource(s string) bool {
	return s == "notion" || s == "pending"
}

// sendTextOutbound envia um texto avulso por uma conversa já conhecida (canal + telefone),
// gravando em outbound_message. Reusado pelo envio manual e pela remarcação.
func (s *Server) sendTextOutbound(ctx context.Context, tenantID TenantID, convID, channel, phone, text string) (string, error) {
	var sender ChatSender
	if channel == "evolution" {
		if s.evoClient == nil || !s.evoClient.Enabled() {
			return "", fmt.Errorf("Evolution API não configurada")
		}
		sender = s.evoClient
	} else {
		if s.sender.accessToken == "" || s.sender.phoneNumberID == "" {
			return "", fmt.Errorf("Meta API não configurada")
		}
		sender = s.sender
	}

	content := MessageContent{Type: "text", Text: text}
	idemKey := newHexID()
	wamid, err := sender.SendMessage(ctx, OutboundMessage{
		TenantID:       tenantID,
		ConversationID: ConversationID(convID),
		Channel:        channel,
		To:             phone,
		Intent:         IntentFreeForm,
		Content:        content,
		IdempotencyKey: idemKey,
	})
	if err != nil {
		return "", err
	}

	contentJSON, _ := json.Marshal(content)
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO outbound_message
			(tenant_id, conversation_id, provider_message_id, idempotency_key, intent_category, content, status, sent_at)
		VALUES ($1, $2::uuid, $3, $4, 'FREE_FORM'::outbound_intent, $5, 'sent', now())
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
	`, tenantID, convID, wamid, idemKey, contentJSON)
	return wamid, nil
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
		SELECT 'in' AS direction, id::text, COALESCE(content->>'Text', '') AS text, received_at AS ts, NULL::jsonb AS reasoning
		FROM inbound_message
		WHERE tenant_id = $1 AND conversation_id = $2::uuid

		UNION ALL

		SELECT 'out', id::text, COALESCE(content->>'Text', ''), sent_at, reasoning
		FROM outbound_message
		WHERE tenant_id = $1 AND conversation_id = $2::uuid AND status = 'sent'

		ORDER BY ts DESC
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
		var rawReasoning []byte
		if err := rows.Scan(&m.Direction, &m.ID, &m.Text, &m.Ts, &rawReasoning); err != nil {
			s.logger.Error("dash: scan message", "err", err)
			continue
		}
		if len(rawReasoning) > 0 {
			r := json.RawMessage(rawReasoning)
			m.Reasoning = &r
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A query pega as 200 MAIS RECENTES (ts DESC) — antes era ts ASC, e numa
	// conversa longa o operador via só as 200 primeiras mensagens, nunca a
	// atual. Inverte aqui para devolver em ordem cronológica, mesmo padrão de
	// MessageRepo.GetRecentTurns (repos.go).
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	jsonOK(w, msgs)
}

// queryInt lê um inteiro da query string com default e faixa [min, max].
func queryInt(r *http.Request, name string, def, min, max int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
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

	// Encontra o número do destinatário E o canal real da conversa — o envio manual
	// do dashboard deve sair pelo MESMO canal por onde o cliente fala (Meta para
	// 'whatsapp', Evolution para 'evolution'), não hardcodar 'whatsapp'/s.sender.
	var phone, channel string
	err := s.pool.QueryRow(ctx, `
		SELECT ci.external_id, conv.channel::text
		FROM conversation conv
		JOIN channel_identity ci ON ci.id = conv.channel_identity_id
		WHERE conv.tenant_id = $1 AND conv.id = $2::uuid
	`, tenantID, convID).Scan(&phone, &channel)
	if err != nil {
		jsonErr(w, "conversation not found", http.StatusNotFound)
		return
	}

	wamid, err := s.sendTextOutbound(ctx, tenantID, convID, channel, phone, body.Text)
	if err != nil {
		s.logger.Error("dash: send message", "err", err)
		jsonErr(w, "falha ao enviar mensagem", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"wamid": wamid})
}

// ── GET /api/config ──────────────────────────────────────────────────────────

func (s *Server) handleDashGetConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)

	var cfg dashConfig
	var allowedRaw []byte
	var adminNumbersRaw []byte
	var captureDisabledRaw []byte
	var qhStart, qhEnd *string
	var kbRaw *string
	// Colunas NOT NULL; lidas em locais e promovidas a ponteiro no JSON, para o
	// painel receber sempre um valor concreto (e o PATCH poder omitir).
	var voiceEnabled bool
	var voiceProvider, voiceID, voiceModel string

	err := s.pool.QueryRow(ctx, `
		SELECT tc.bot_name, tc.bot_gender, tc.bot_enabled_by_default,
		       tc.bot_allowed_numbers,
		       tc.quiet_hours->>'start', tc.quiet_hours->>'end',
		       tc.kb_content::text,
		       tc.system_prompt,
		       tc.admin_system_prompt,
		       tc.admin_whatsapp_numbers,
		       tc.debounce_ms,
		       tc.evolution_bot_reply_enabled,
		       tc.evolution_lead_capture_enabled,
		       tc.evolution_capture_disabled,
		       tc.notif_phone,
		       tc.notif_instance,
		       tc.notif_enabled,
		       tc.notif_on_success,
		       tc.notif_on_container_down,
		       tc.voice_enabled,
		       tc.voice_provider,
		       tc.voice_id,
		       tc.voice_model
		FROM tenant_config tc
		WHERE tc.tenant_id = $1
	`, tenantID).Scan(
		&cfg.BotName, &cfg.BotGender, &cfg.BotEnabledByDefault,
		&allowedRaw, &qhStart, &qhEnd, &kbRaw, &cfg.SystemPrompt,
		&cfg.AdminSystemPrompt,
		&adminNumbersRaw, &cfg.DebounceMs, &cfg.EvolutionBotReplyEnabled,
		&cfg.EvolutionLeadCaptureEnabled, &captureDisabledRaw,
		&cfg.NotifPhone, &cfg.NotifInstance, &cfg.NotifEnabled,
		&cfg.NotifOnSuccess, &cfg.NotifOnContainerDown,
		&voiceEnabled, &voiceProvider, &voiceID, &voiceModel,
	)
	if err != nil {
		s.logger.Error("dash: get config", "err", err)
		jsonErr(w, "config not found", http.StatusNotFound)
		return
	}

	if len(captureDisabledRaw) > 0 {
		_ = json.Unmarshal(captureDisabledRaw, &cfg.EvolutionCaptureDisabled)
	}
	if cfg.EvolutionCaptureDisabled == nil {
		cfg.EvolutionCaptureDisabled = []string{}
	}

	if len(allowedRaw) > 0 {
		_ = json.Unmarshal(allowedRaw, &cfg.BotAllowedNumbers)
	}
	if cfg.BotAllowedNumbers == nil {
		cfg.BotAllowedNumbers = []string{}
	}
	if len(adminNumbersRaw) > 0 {
		_ = json.Unmarshal(adminNumbersRaw, &cfg.AdminWhatsAppNumbers)
	}
	if cfg.AdminWhatsAppNumbers == nil {
		cfg.AdminWhatsAppNumbers = []string{}
	}
	cfg.QuietHoursStart = qhStart
	cfg.QuietHoursEnd = qhEnd
	cfg.VoiceEnabled = &voiceEnabled
	cfg.VoiceProvider = &voiceProvider
	cfg.VoiceID = &voiceID
	cfg.VoiceModel = &voiceModel

	if kbRaw != nil && *kbRaw != "" && *kbRaw != "null" {
		_ = json.Unmarshal([]byte(*kbRaw), &cfg.KBContent)
	}
	if cfg.KBContent == nil {
		cfg.KBContent = []KBEntry{}
	}

	// Se ainda não houver prompt customizado, mostra o prompt padrão (do código)
	// já preenchido e editável no dashboard. O bot continua usando esse mesmo
	// texto por padrão, então preencher não muda o comportamento.
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultPersonaPrompt(
			TenantConfig{BotName: cfg.BotName, BotGender: cfg.BotGender},
			ConversationContext{},
		)
	}
	if cfg.AdminSystemPrompt == "" {
		cfg.AdminSystemPrompt = DefaultAdminPrompt()
	}

	jsonOK(w, cfg)
}

// ── GET /api/config/default-prompt ───────────────────────────────────────────
// Retorna o prompt de identidade+estilo padrão (que está no código) para o
// dashboard importar no campo editável "Prompt do sistema".

func (s *Server) handleDashDefaultPrompt(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)

	var botName, botGender string
	if err := s.pool.QueryRow(ctx,
		`SELECT bot_name, bot_gender FROM tenant_config WHERE tenant_id = $1`, tenantID,
	).Scan(&botName, &botGender); err != nil {
		s.logger.Error("dash: default-prompt", "err", err)
		jsonErr(w, "config not found", http.StatusNotFound)
		return
	}

	cfg := TenantConfig{BotName: botName, BotGender: botGender}
	jsonOK(w, map[string]string{"prompt": DefaultPersonaPrompt(cfg, ConversationContext{})})
}

// ── GET /api/config/default-admin-prompt ─────────────────────────────────────
// Retorna a parte editável padrão (do código) do prompt do admin.

func (s *Server) handleDashDefaultAdminPrompt(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{"prompt": DefaultAdminPrompt()})
}

// ── PATCH /api/config ────────────────────────────────────────────────────────

// dashConfigPatch — corpo do PATCH de configuração.
//
// TUDO anulável de propósito: PATCH quer dizer "mude o que eu mandei", não
// "substitua o registro". Campo ausente chega nil, e o COALESCE no UPDATE
// preserva o que já está no banco.
//
// Separado do dashConfig (que o GET devolve) porque só o corpo do PATCH precisa
// distinguir "não mandei" de "mandei vazio".
//
// Antes, uma chamada legítima mandando só o systemPrompt zerava a base de
// conhecimento, o nome do bot, a allowlist e os números dos admins — e o bot
// parava de responder a todo mundo, sem erro nenhum, porque o UPDATE gravava o
// zero value de cada campo omitido. Aconteceu em produção. A lição já estava
// escrita neste arquivo, para os campos de voz; faltava valer para os outros.
type dashConfigPatch struct {
	BotName                     *string    `json:"botName"`
	BotGender                   *string    `json:"botGender"`
	BotEnabledByDefault         *bool      `json:"botEnabledByDefault"`
	BotAllowedNumbers           *[]string  `json:"botAllowedNumbers"`
	QuietHoursStart             *string    `json:"quietHoursStart"`
	QuietHoursEnd               *string    `json:"quietHoursEnd"`
	KBContent                   *[]KBEntry `json:"kbContent"`
	SystemPrompt                *string    `json:"systemPrompt"`
	AdminSystemPrompt           *string    `json:"adminSystemPrompt"`
	AdminWhatsAppNumbers        *[]string  `json:"adminWhatsAppNumbers"`
	DebounceMs                  *int       `json:"debounceMs"`
	EvolutionBotReplyEnabled    *bool      `json:"evolutionBotReplyEnabled"`
	EvolutionLeadCaptureEnabled *bool      `json:"evolutionLeadCaptureEnabled"`
	EvolutionCaptureDisabled    *[]string  `json:"evolutionCaptureDisabled"`
	NotifPhone                  *string    `json:"notifPhone"`
	NotifInstance               *string    `json:"notifInstance"`
	NotifEnabled                *bool      `json:"notifEnabled"`
	NotifOnSuccess              *bool      `json:"notifOnSuccess"`
	NotifOnContainerDown        *bool      `json:"notifOnContainerDown"`
	VoiceEnabled                *bool      `json:"voiceEnabled"`
	VoiceProvider               *string    `json:"voiceProvider"`
	VoiceID                     *string    `json:"voiceId"`
	VoiceModel                  *string    `json:"voiceModel"`
}

// jsonbOuNil devolve o JSON de uma lista, ou nil quando ela nem veio.
//
// A diferença importa: nil é "não mexe"; uma lista vazia enviada de propósito
// é "esvazie mesmo".
func jsonbOuNil[T any](v *[]T) *string {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(*v)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

func (s *Server) handleDashPatchConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)

	var body dashConfigPatch
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, "invalid body", http.StatusBadRequest)
		return
	}

	// Listas: nil = não mandou (preserva); [] = mandou vazio (esvazia mesmo).
	allowedJSON := jsonbOuNil(body.BotAllowedNumbers)
	adminNumbersJSON := jsonbOuNil(body.AdminWhatsAppNumbers)
	captureDisabledJSON := jsonbOuNil(body.EvolutionCaptureDisabled)
	kbJSON := jsonbOuNil(body.KBContent)

	// Mantém a coluna legada em sincronia (primeiro número da lista).
	var legacyAdmin *string
	if body.AdminWhatsAppNumbers != nil {
		v := ""
		if lista := *body.AdminWhatsAppNumbers; len(lista) > 0 {
			v = lista[0]
		}
		legacyAdmin = &v
	}

	// voice_provider: só os implementados. Valor desconhecido vira "openai",
	// porque a constraint do banco rejeitaria e derrubaria o PATCH inteiro.
	var voiceProvider *string
	if body.VoiceProvider != nil {
		p := *body.VoiceProvider
		if p != "elevenlabs" && p != "clips" {
			p = "openai"
		}
		voiceProvider = &p
	}

	// debounce_ms: clamp defensivo (0–15s; 0 = sem agrupamento).
	var debounceMs *int
	if body.DebounceMs != nil {
		d := *body.DebounceMs
		if d < 0 {
			d = 0
		}
		if d > 15000 {
			d = 15000
		}
		debounceMs = &d
	}

	// quiet_hours só é tocado quando os DOIS extremos vêm; mandar um só não
	// diz nada sobre a janela.
	var quietHoursJSON *string
	if body.QuietHoursStart != nil && body.QuietHoursEnd != nil {
		v := "{}"
		if *body.QuietHoursStart != "" && *body.QuietHoursEnd != "" {
			v = fmt.Sprintf(`{"start":%q,"end":%q}`, *body.QuietHoursStart, *body.QuietHoursEnd)
		}
		quietHoursJSON = &v
	}

	// COALESCE em TUDO: o que não veio no corpo fica como está.
	_, err := s.pool.Exec(ctx, `
		UPDATE tenant_config
		SET bot_name               = COALESCE($1, bot_name),
		    bot_gender             = COALESCE($2, bot_gender),
		    bot_enabled_by_default = COALESCE($3, bot_enabled_by_default),
		    bot_allowed_numbers    = COALESCE($4::jsonb, bot_allowed_numbers),
		    quiet_hours            = COALESCE($5::jsonb, quiet_hours),
		    kb_content             = COALESCE($6::jsonb, kb_content),
		    system_prompt          = COALESCE($8, system_prompt),
		    admin_whatsapp_number  = COALESCE($9, admin_whatsapp_number),
		    admin_whatsapp_numbers = COALESCE($10::jsonb, admin_whatsapp_numbers),
		    debounce_ms            = COALESCE($11, debounce_ms),
		    admin_system_prompt    = COALESCE($12, admin_system_prompt),
		    evolution_bot_reply_enabled    = COALESCE($13, evolution_bot_reply_enabled),
		    evolution_lead_capture_enabled = COALESCE($14, evolution_lead_capture_enabled),
		    evolution_capture_disabled     = COALESCE($15::jsonb, evolution_capture_disabled),
		    notif_phone    = COALESCE($16, notif_phone),
		    notif_instance = COALESCE($17, notif_instance),
		    notif_enabled  = COALESCE($18, notif_enabled),
		    notif_on_success = COALESCE($19, notif_on_success),
		    notif_on_container_down = COALESCE($20, notif_on_container_down),
		    voice_enabled  = COALESCE($21, voice_enabled),
		    voice_provider = COALESCE($22, voice_provider),
		    voice_id       = COALESCE($23, voice_id),
		    voice_model    = COALESCE($24, voice_model),
		    updated_at     = now()
		WHERE tenant_id = $7
	`, body.BotName, body.BotGender, body.BotEnabledByDefault,
		allowedJSON, quietHoursJSON, kbJSON, tenantID, body.SystemPrompt,
		legacyAdmin, adminNumbersJSON, debounceMs, body.AdminSystemPrompt,
		body.EvolutionBotReplyEnabled, body.EvolutionLeadCaptureEnabled, captureDisabledJSON,
		body.NotifPhone, body.NotifInstance, body.NotifEnabled,
		body.NotifOnSuccess, body.NotifOnContainerDown,
		body.VoiceEnabled, voiceProvider, body.VoiceID, body.VoiceModel)
	if err != nil {
		s.logger.Error("dash: patch config", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Invalida o cache de debounce_ms (config mudou). Fail-open: o TTL curto
	// também garante consistência eventual se o DEL falhar.
	s.tenantCache.InvalidateDebounce(ctx, string(tenantID))
	jsonOK(w, map[string]bool{"ok": true})
}

// ── DELETE /api/conversations/{id} ──────────────────────────────────────────

func (s *Server) handleDashDeleteConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)
	convID := r.PathValue("id")
	if convID == "" {
		jsonErr(w, "missing id", http.StatusBadRequest)
		return
	}

	_, err := s.pool.Exec(ctx, `
		DELETE FROM conversation
		WHERE tenant_id = $1 AND id = $2::uuid
	`, tenantID, convID)
	if err != nil {
		s.logger.Error("dash: delete conversation", "err", err)
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

// ── GET /api/logs ─────────────────────────────────────────────────────────────

func (s *Server) handleDashLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)
	q := r.URL.Query()

	page := atoiDefault(q.Get("page"), 1)
	pageSize := atoiDefault(q.Get("pageSize"), 50)

	filters := LogFilters{
		Answered:       triBool(q.Get("answered")),
		AnsweredFromKb: triBool(q.Get("kb")),
		Handoff:        triBool(q.Get("handoff")),
		ErrorsOnly:     q.Get("errors") == "true",
		Search:         strings.TrimSpace(q.Get("q")),
	}

	entries, total, err := s.logRepo.ListPaged(ctx, tenantID, filters, page, pageSize)
	if err != nil {
		s.logger.Error("dash: list logs", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}

	logs := make([]dashProcessingLog, 0, len(entries))
	for _, e := range entries {
		logs = append(logs, toDashLog(e))
	}
	jsonOK(w, map[string]any{
		"logs":     logs,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// triBool converte "true"/"false" em *bool; qualquer outra coisa → nil (sem filtro).
func triBool(v string) *bool {
	switch v {
	case "true":
		t := true
		return &t
	case "false":
		f := false
		return &f
	default:
		return nil
	}
}

// atoiDefault converte uma string para int, retornando def em caso de erro.
func atoiDefault(v string, def int) int {
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return def
}

// ── CRM: leads ────────────────────────────────────────────────────────────────

type dashLead struct {
	ID             string     `json:"id"`
	ContactName    string     `json:"contactName"`
	Phone          string     `json:"phone"`
	Status         string     `json:"status"`
	Interest       string     `json:"interest"`
	Owner          string     `json:"owner"`
	Origin         string     `json:"origin"`
	ConversationID string     `json:"conversationId"`
	LastActivity   *time.Time `json:"lastActivity"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// leadFunnelStatuses — estágios válidos do funil do CRM.
var leadFunnelStatuses = map[string]bool{
	"novo": true, "em_atendimento": true, "aula_marcada": true,
	"matriculado": true, "perdido": true,
}

// GET /api/leads — lista o funil de leads do WhatsApp.
//
// Paginado: a query tem dois LATERAL por linha, então sem LIMIT ela cresce
// linearmente com a base de leads. Aceita ?limit= e ?offset=; o formato da
// resposta segue sendo um array, como nas outras listagens do painel.
func (s *Server) handleDashLeads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)
	limit := queryInt(r, "limit", 200, 1, 500)
	offset := queryInt(r, "offset", 0, 0, 1_000_000)

	rows, err := s.pool.Query(ctx, `
		SELECT l.id::text, COALESCE(ct.display_name, ''), COALESCE(ci.external_id, ''),
		       l.status, COALESCE(l.interest, ''), COALESCE(l.owner, ''), COALESCE(l.origin, 'oficial'),
		       COALESCE(cv.id::text, ''), cv.last_inbound_at, l.created_at
		FROM lead l
		JOIN contact ct ON ct.tenant_id = l.tenant_id AND ct.id = l.contact_id
		LEFT JOIN LATERAL (
			SELECT external_id FROM channel_identity
			WHERE tenant_id = l.tenant_id AND contact_id = l.contact_id LIMIT 1
		) ci ON true
		LEFT JOIN LATERAL (
			-- conversation não tem contact_id direto — o vínculo é indireto via
			-- channel_identity_id (um contato pode ter mais de um canal/número;
			-- pega a conversa mais recente entre todos eles).
			SELECT id, last_inbound_at FROM conversation
			WHERE tenant_id = l.tenant_id AND channel_identity_id IN (
				SELECT id FROM channel_identity
				WHERE tenant_id = l.tenant_id AND contact_id = l.contact_id
			)
			ORDER BY last_inbound_at DESC NULLS LAST LIMIT 1
		) cv ON true
		WHERE l.tenant_id = $1
		ORDER BY cv.last_inbound_at DESC NULLS LAST, l.created_at DESC, l.id DESC
		LIMIT $2 OFFSET $3
	`, tenantID, limit, offset)
	if err != nil {
		s.logger.Error("dash: list leads", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := make([]dashLead, 0)
	for rows.Next() {
		var l dashLead
		if err := rows.Scan(&l.ID, &l.ContactName, &l.Phone, &l.Status,
			&l.Interest, &l.Owner, &l.Origin, &l.ConversationID, &l.LastActivity, &l.CreatedAt); err != nil {
			s.logger.Error("dash: scan lead", "err", err)
			jsonErr(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Normaliza status legado para o funil.
		if !leadFunnelStatuses[l.Status] {
			l.Status = "novo"
		}
		out = append(out, l)
	}
	jsonOK(w, out)
}

// GET /api/evolution/instances — status dos números conectados na Evolution.
// ── GET /api/audio/gaps ──────────────────────────────────────────────────────
//
// Lista os momentos em que o bot quis falar e não tinha gravação, mais
// frequentes primeiro. É a fila de gravação: cada linha é um áudio que vale
// gravar, ordenada pelo que mais faz falta na prática.
func (s *Server) handleDashAudioGaps(w http.ResponseWriter, r *http.Request) {
	if s.audioClips == nil || !s.audioClips.Enabled() {
		jsonOK(w, []AudioGapRow{})
		return
	}
	incluirResolvidas := r.URL.Query().Get("all") == "true"
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	gaps, err := s.audioClips.ListGaps(r.Context(), TenantID(s.cfg.TenantID), incluirResolvidas, limit)
	if err != nil {
		s.logger.Error("dash: audio gaps", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, gaps)
}

// ── POST /api/audio/gaps/{id}/resolve ────────────────────────────────────────
//
// Marca a lacuna como resolvida, depois que o áudio foi gravado e importado.
func (s *Server) handleDashResolveAudioGap(w http.ResponseWriter, r *http.Request) {
	if s.audioClips == nil || !s.audioClips.Enabled() {
		jsonErr(w, "audio clips not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		jsonErr(w, "missing id", http.StatusBadRequest)
		return
	}
	if err := s.audioClips.ResolveGap(r.Context(), TenantID(s.cfg.TenantID), id); err != nil {
		s.logger.Error("dash: resolve audio gap", "err", err, "id", id)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *Server) handleDashEvolutionInstances(w http.ResponseWriter, r *http.Request) {
	if s.evoClient == nil {
		jsonOK(w, []EvolutionInstance{})
		return
	}
	insts, err := s.evoClient.Instances(r.Context())
	if err != nil {
		s.logger.Error("dash: evolution instances", "err", err)
		jsonOK(w, []EvolutionInstance{})
		return
	}
	jsonOK(w, insts)
}

// ── Agendamentos (aula experimental) ────────────────────────────────────────

type dashAgenda struct {
	Upcoming []dashUpcoming `json:"upcoming"`
	Pending  []dashPending  `json:"pending"`
}

// dashUpcoming — aula experimental já agendada no Notion.
// dashUpcoming — uma linha da grade semanal, como o painel mostra.
//
// Acompanha a base real: dia da semana e horário em texto, não data-e-hora. O
// painel exibe a mesma coisa que a escola vê no Notion.
type dashUpcoming struct {
	PageID    string `json:"pageId"`
	Titulo    string `json:"titulo"`
	Dia       string `json:"dia"`
	Horario   string `json:"horario"`
	Display   string `json:"display"`
	Professor string `json:"professor"`
	Conteudo  string `json:"conteudo"`
	// DoBot — a escola precisa distinguir, de relance, o que o bot marcou.
	DoBot bool `json:"doBot"`
}

// dashPending — agendamento aguardando confirmação do admin (ainda não no Notion).
type dashPending struct {
	ID           string    `json:"id"`
	Aluno        string    `json:"aluno"`
	Phone        string    `json:"phone"`
	Kind         string    `json:"kind"`
	Course       string    `json:"course"`
	ProposedDay  string    `json:"proposedDay"`
	ProposedDate string    `json:"proposedDate"`
	ProposedTime string    `json:"proposedTime"`
	CreatedAt    time.Time `json:"createdAt"`
}

// GET /api/bookings — agenda de aulas experimentais (Notion) + pendências.
func (s *Server) handleDashBookings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)

	out := dashAgenda{Upcoming: []dashUpcoming{}, Pending: []dashPending{}}

	if n := s.engine.deps.Notion; n != nil && n.Enabled() {
		agenda, _ := n.Schedule(ctx)
		for _, e := range agenda {
			out.Upcoming = append(out.Upcoming, dashUpcoming{
				PageID: e.PageID, Titulo: e.Titulo,
				Dia: e.Dia, Horario: e.Horario, Display: e.Display(),
				Professor: e.Professor, Conteudo: e.Conteudo,
				DoBot: EhDoBot(e.Titulo),
			})
		}
	}

	if s.engine.deps.Bookings != nil {
		var pend []PendingBooking
		if err := s.withTenant(ctx, func(tx pgx.Tx) error {
			p, err := s.engine.deps.Bookings.ListOpen(ctx, tx, tenantID)
			if err != nil {
				return err
			}
			pend = p
			return nil
		}); err != nil {
			s.logger.Error("dash: list pending bookings", "err", err)
		}
		for _, b := range pend {
			out.Pending = append(out.Pending, dashPending{
				ID: b.ID, Aluno: bookingAluno(b), Phone: b.ClientPhone,
				Kind: b.Kind, Course: b.Course,
				ProposedDay: b.ProposedDay, ProposedDate: b.ProposedDate, ProposedTime: b.ProposedTime,
				CreatedAt: b.CreatedAt,
			})
		}
	}

	jsonOK(w, out)
}

// convByPhone acha a conversa mais recente de um telefone (qualquer canal), comparando
// só os dígitos. Usado na remarcação de aulas vindas do Notion (sem conversationId).
func (s *Server) convByPhone(ctx context.Context, tenantID TenantID, phone string) (convID, channel string, err error) {
	digits := normalizePhone(phone)
	if digits == "" {
		return "", "", fmt.Errorf("telefone vazio")
	}
	err = s.pool.QueryRow(ctx, `
		SELECT conv.id::text, conv.channel::text
		FROM conversation conv
		JOIN channel_identity ci ON ci.id = conv.channel_identity_id
		WHERE conv.tenant_id = $1
		  AND regexp_replace(ci.external_id, '\D', '', 'g') = $2
		ORDER BY conv.last_inbound_at DESC NULLS LAST
		LIMIT 1
	`, tenantID, digits).Scan(&convID, &channel)
	return convID, channel, err
}

// POST /api/bookings/reschedule — remarca uma aula (Notion ou pendência) e, se houver
// message, avisa o aluno pelo WhatsApp (best-effort). Body:
//
//	{ source: "notion"|"pending", id, date: "YYYY-MM-DD", time: "HH:MM", phone, message }
func (s *Server) handleDashReschedule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)

	var body struct {
		Source  string `json:"source"`
		ID      string `json:"id"`
		Date    string `json:"date"`
		Time    string `json:"time"`
		Phone   string `json:"phone"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !validRescheduleSource(body.Source) || body.ID == "" {
		jsonErr(w, "invalid source/id", http.StatusBadRequest)
		return
	}
	iso, ok := ResolveBookingDateTime(body.Date, body.Time, time.Now())
	if !ok {
		jsonErr(w, "data/hora inválida", http.StatusBadRequest)
		return
	}
	// Normaliza date/time a partir do ISO resolvido — mesma fonte de verdade do
	// caminho Notion (evita gravar string crua/livre em pending_booking).
	resolved, _ := time.Parse(time.RFC3339, iso)
	resolvedDate := resolved.In(brLocation).Format("2006-01-02")
	resolvedTime := resolved.In(brLocation).Format("15:04")

	// 1) Persistir a remarcação.
	switch body.Source {
	case "notion":
		n := s.engine.deps.Notion
		if n == nil || !n.Enabled() {
			jsonErr(w, "Notion não configurado", http.StatusServiceUnavailable)
			return
		}
		if err := n.UpdateBookingDateTime(ctx, body.ID, iso); err != nil {
			s.logger.Error("dash: reschedule notion", "err", err)
			jsonErr(w, "falha ao remarcar no Notion", http.StatusInternalServerError)
			return
		}
	case "pending":
		if s.engine.deps.Bookings == nil {
			jsonErr(w, "pendências indisponíveis", http.StatusServiceUnavailable)
			return
		}
		if err := s.withTenant(ctx, func(tx pgx.Tx) error {
			return s.engine.deps.Bookings.UpdateProposed(ctx, tx, tenantID, body.ID, resolvedDate, resolvedTime)
		}); err != nil {
			s.logger.Error("dash: reschedule pending", "err", err)
			jsonErr(w, "falha ao remarcar pendência", http.StatusInternalServerError)
			return
		}
	}

	// 2) Avisar o aluno (best-effort: não desfaz a remarcação se falhar).
	resp := map[string]any{"ok": true, "notified": false}
	if strings.TrimSpace(body.Message) != "" {
		convID, channel, err := s.convByPhone(ctx, tenantID, body.Phone)
		if err != nil {
			// Best-effort: a remarcação já foi persistida. Loga (inclui erro real de
			// DB, não só "não encontrado") e responde sem derrubar a remarcação.
			s.logger.Error("dash: reschedule notify lookup", "phone", body.Phone, "err", err)
			resp["notifyError"] = "conversa não encontrada para o número"
			jsonOK(w, resp)
			return
		}
		if _, err := s.sendTextOutbound(ctx, tenantID, convID, channel, body.Phone, body.Message); err != nil {
			resp["notifyError"] = err.Error()
			jsonOK(w, resp)
			return
		}
		resp["notified"] = true
	}
	jsonOK(w, resp)
}

// PATCH /api/leads/{id} — atualiza status (e opcional owner/interest) de um lead.
func (s *Server) handleDashPatchLead(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)
	id := r.PathValue("id")

	var body struct {
		Status   *string `json:"status"`
		Owner    *string `json:"owner"`
		Interest *string `json:"interest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Status != nil && !leadFunnelStatuses[*body.Status] {
		jsonErr(w, "invalid status", http.StatusBadRequest)
		return
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE lead SET
		  status   = COALESCE($3, status),
		  owner    = COALESCE($4, owner),
		  interest = COALESCE($5, interest),
		  updated_at = now()
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id, body.Status, body.Owner, body.Interest)
	if err != nil {
		s.logger.Error("dash: patch lead", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, "lead not found", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

// DELETE /api/leads/{id} — remove um lead do funil (não toca em conversa/contato).
func (s *Server) handleDashDeleteLead(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := TenantID(s.cfg.TenantID)
	id := r.PathValue("id")

	tag, err := s.pool.Exec(ctx,
		`DELETE FROM lead WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		s.logger.Error("dash: delete lead", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, "lead not found", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func toDashLog(e ProcessingLogEntry) dashProcessingLog {
	cited := e.CitedEntryIDs
	if cited == nil {
		cited = []string{}
	}
	bubbles := e.Bubbles
	if bubbles == nil {
		bubbles = []string{}
	}
	kind := e.Kind
	if kind == "" {
		kind = "message"
	}
	return dashProcessingLog{
		ID:             e.ID,
		Kind:           kind,
		ConversationID: e.ConversationID,
		ContactPhone:   e.ContactPhone,
		ContactName:    e.ContactName,
		InboundText:    e.InboundText,
		Answered:       e.Answered,
		AnsweredFromKb: e.AnsweredFromKb,
		Handoff:        e.Handoff,
		CitedEntryIDs:  cited,
		Bubbles:        bubbles,
		ToolCalls:      e.ToolCalls,
		ProcessingMs:   e.ProcessingMs,
		Error:          e.Error,
		CreatedAt:      e.CreatedAt,
	}
}

// ── GET /api/ws ───────────────────────────────────────────────────────────────
// Upgrade para WebSocket. Auth por (a) cookie de sessão de admin — o browser
// manda cookies no handshake automaticamente, é o caminho do painel — ou
// (b) DASH_API_KEY via sub-protocol "dash, <key>" (browser JS não suporta
// headers customizados em WebSocket), para integrações server-to-server.
//
// NÃO aceita ?key=: query string vaza para o log de acesso, para o Traefik e
// para o histórico do browser, e o golog não redige a chave "key".

func (s *Server) handleDashWS(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		http.NotFound(w, r)
		return
	}
	if !s.dashWSAuthorized(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	s.hub.Upgrade(w, r)
}

// dashWSAuthorized replica dashAuthorized, mas lendo a DASH_API_KEY do
// sub-protocol/query em vez do header (limitação da API WebSocket do browser).
func (s *Server) dashWSAuthorized(r *http.Request) bool {
	if s.cfg.DashAPIKey != "" {
		// Browser manda a chave como sub-protocol: "dash, <key>".
		var key string
		for _, p := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
			if p = strings.TrimSpace(p); p != "" && p != "dash" {
				key = p
			}
		}
		if key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.DashAPIKey)) == 1 {
			return true
		}
	}
	return s.session.Authorized(r)
}

// Garante que context é usado (lint).
var _ = context.Background

// ── qualificação do lead (0037) ──────────────────────────────────────────────

// dashQualificacao — o dossiê como o painel mostra.
type dashQualificacao struct {
	Phone           string `json:"phone"`
	ContactName     string `json:"contactName"`
	Grau            string `json:"grau"`
	GrauLegivel     string `json:"grauLegivel"`
	ParaQuem        string `json:"paraQuem"`
	AlunoNome       string `json:"alunoNome"`
	AlunoIdade      int    `json:"alunoIdade"`
	Interesse       string `json:"interesse"`
	JaFazCurso      string `json:"jaFazCurso"`
	Disponibilidade string `json:"disponibilidade"`
	Motivacao       string `json:"motivacao"`
	MotivacaoTipo   string `json:"motivacaoTipo"`
	Observacoes     string `json:"observacoes"`
	PrecoInformado  bool   `json:"precoInformado"`
	AulaMarcada     bool   `json:"aulaMarcada"`
	Respondidas     int    `json:"respondidas"`
	AtualizadoEm    string `json:"atualizadoEm"`
}

// GET /api/qualificacoes — quem é cada lead, do mais quente para o mais frio.
//
// A ordem não é cronológica de propósito: quem abre esta tela quer saber para
// quem ligar primeiro, e isso é o grau, não a hora da última mensagem.
func (s *Server) handleDashQualificacoes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, `
		SELECT ci.external_id, coalesce(c.display_name, ''),
		       q.para_quem, q.aluno_nome, q.aluno_idade, q.interesse, q.ja_faz_curso,
		       q.disponibilidade, q.motivacao, q.motivacao_tipo, q.observacoes,
		       q.preco_informado, q.aula_marcada, q.atualizado_em
		FROM lead_qualificacao q
		JOIN contact c ON c.id = q.contact_id
		JOIN channel_identity ci ON ci.contact_id = c.id
		WHERE q.tenant_id = $1
		ORDER BY q.atualizado_em DESC
		LIMIT 500
	`, TenantID(s.cfg.TenantID))
	if err != nil {
		s.logger.Error("dash: listar qualificações", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []dashQualificacao{}
	vistos := map[string]bool{}
	for rows.Next() {
		var d dashQualificacao
		var q Qualificacao
		var atualizado time.Time
		if err := rows.Scan(&d.Phone, &d.ContactName,
			&q.ParaQuem, &q.AlunoNome, &q.AlunoIdade, &q.Interesse, &q.JaFazCurso,
			&q.Disponibilidade, &q.Motivacao, &q.MotivacaoTipo, &q.Observacoes,
			&q.PrecoInformado, &q.AulaMarcada, &atualizado); err != nil {
			s.logger.Error("dash: scan qualificação", "err", err)
			continue
		}
		// Um contato pode ter identidade em mais de um canal (whatsapp e
		// evolution). É a mesma pessoa; mostrar duas vezes só confunde.
		if vistos[d.Phone] {
			continue
		}
		vistos[d.Phone] = true

		d.Grau = string(q.Grau())
		d.GrauLegivel = q.Grau().Legivel()
		d.ParaQuem, d.AlunoNome, d.AlunoIdade = q.ParaQuem, q.AlunoNome, q.AlunoIdade
		d.Interesse, d.JaFazCurso = q.Interesse, q.JaFazCurso
		d.Disponibilidade, d.Motivacao = q.Disponibilidade, q.Motivacao
		d.MotivacaoTipo, d.Observacoes = q.MotivacaoTipo, q.Observacoes
		d.PrecoInformado, d.AulaMarcada = q.PrecoInformado, q.AulaMarcada
		d.Respondidas = q.Respondidas()
		d.AtualizadoEm = atualizado.Format(time.RFC3339)
		out = append(out, d)
	}

	// Mais quente primeiro; dentro do mesmo grau, o mais recente.
	peso := map[string]int{
		string(GrauMuitoQualificado): 0, string(GrauQualificado): 1,
		string(GrauMorno): 2, string(GrauFrio): 3,
	}
	sort.SliceStable(out, func(i, j int) bool {
		if peso[out[i].Grau] != peso[out[j].Grau] {
			return peso[out[i].Grau] < peso[out[j].Grau]
		}
		return out[i].AtualizadoEm > out[j].AtualizadoEm
	})
	jsonOK(w, map[string]any{"leads": out})
}

// GET /api/clientes/{telefone}.md — o dossiê de uma pessoa, em Markdown.
//
// Serve texto puro, não JSON: a ideia é abrir, ler, e colar num WhatsApp ou
// num documento. Gerado do banco a cada chamada, então nunca está velho.
func (s *Server) handleDossieMarkdown(w http.ResponseWriter, r *http.Request) {
	if s.engine.deps.Qualificacoes == nil {
		jsonErr(w, "qualificação não configurada", http.StatusServiceUnavailable)
		return
	}
	telefone := strings.TrimSuffix(r.PathValue("telefone"), ".md")
	telefone = soDigitos(telefone)
	if telefone == "" {
		jsonErr(w, "telefone inválido", http.StatusBadRequest)
		return
	}
	d, ok := s.engine.deps.Qualificacoes.DossieDoTelefone(r.Context(), TenantID(s.cfg.TenantID), telefone)
	if !ok {
		http.Error(w, "# Não encontrei ninguém com esse número\n", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=\""+d.NomeDoArquivo()+"\"")
	_, _ = w.Write([]byte(d.Markdown(time.Now())))
}

// soDigitos tira tudo que não for número — o telefone chega de jeitos
// diferentes conforme quem chama (com +, com espaço, com parêntese).
func soDigitos(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}
