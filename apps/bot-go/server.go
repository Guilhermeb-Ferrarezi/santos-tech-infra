package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// debouncer — agrupa chamadas por chave e dispara após um delay de silêncio.
// ---------------------------------------------------------------------------

type debouncer struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

func newDebouncer() *debouncer {
	return &debouncer{timers: make(map[string]*time.Timer)}
}

// debounce agenda fn para rodar delay após a última chamada com a mesma chave.
// Se chamado novamente antes do disparo, reinicia o timer (reset).
func (d *debouncer) debounce(key string, delay time.Duration, fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if t, ok := d.timers[key]; ok {
		t.Stop()
	}

	d.timers[key] = time.AfterFunc(delay, func() {
		d.mu.Lock()
		delete(d.timers, key)
		d.mu.Unlock()
		fn()
	})
}

// ---------------------------------------------------------------------------
// burstBuffer — acumula as mensagens de uma rajada por chave (ExternalID) e
// SERIALIZA o processamento por chave. Só um Handle roda por conversa de cada
// vez; mensagens que chegam durante o processamento entram numa única resposta
// de continuação depois (coalescing) — evita respostas concorrentes e repetidas.
// ---------------------------------------------------------------------------

type burstItem struct {
	msg     InboundMessage
	eventID WebhookEventID
}

type burstBuffer struct {
	mu         sync.Mutex
	items      map[string][]burstItem
	processing map[string]bool
}

func newBurstBuffer() *burstBuffer {
	return &burstBuffer{
		items:      make(map[string][]burstItem),
		processing: make(map[string]bool),
	}
}

func (b *burstBuffer) add(key string, it burstItem) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items[key] = append(b.items[key], it)
}

// beginOrQueue tenta assumir o processamento da chave. Se já há um Handle em
// andamento para ela, retorna nil (as mensagens ficam no buffer e serão pegas
// pelo loop atual). Senão marca como em processamento e devolve os itens.
func (b *burstBuffer) beginOrQueue(key string) []burstItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.processing[key] {
		return nil
	}
	items := b.items[key]
	if len(items) == 0 {
		return nil
	}
	delete(b.items, key)
	b.processing[key] = true
	return items
}

// next é chamado ao terminar um Handle. Se chegaram mensagens novas durante o
// processamento, devolve-as (o loop continua, mantendo o lock). Senão libera a
// chave e retorna nil.
func (b *burstBuffer) next(key string) []burstItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	items := b.items[key]
	if len(items) == 0 {
		delete(b.processing, key)
		return nil
	}
	delete(b.items, key)
	return items
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Server encapsula as dependências do servidor HTTP.
type Server struct {
	cfg     Config
	engine  *ConversationEngine
	webhook *WebhookRepo
	pool    *pgxpool.Pool
	sender  *WhatsAppSender
	logger  *slog.Logger
	dbnc    *debouncer
	burst   *burstBuffer
	hub     *WSHub
	logRepo *ProcessingLogRepo
	// Captura de leads + resposta da Evolution (webhook + segundo engine).
	contacts   *ContactRepo
	leads      *LeadRepo
	withTenant func(ctx context.Context, fn func(pgx.Tx) error) error
	evoEngine  *ConversationEngine
	evoClient  *EvolutionClient
	voice      *VoiceClient
	// rdb pode ser nil — o dedupe de notificações cai pro fallback in-memory.
	rdb         *redis.Client
	notifDedupe *notifDedupe
}

// NewServer cria um Server com as dependências fornecidas.
func NewServer(cfg Config, engine *ConversationEngine, webhook *WebhookRepo, pool *pgxpool.Pool, sender *WhatsAppSender, logger *slog.Logger, hub *WSHub, logRepo *ProcessingLogRepo, evoEngine *ConversationEngine, evoClient *EvolutionClient, voice *VoiceClient, rdb *redis.Client) *Server {
	return &Server{
		cfg:         cfg,
		engine:      engine,
		webhook:     webhook,
		pool:        pool,
		sender:      sender,
		logger:      logger,
		dbnc:        newDebouncer(),
		burst:       newBurstBuffer(),
		hub:         hub,
		logRepo:     logRepo,
		contacts:    NewContactRepo(pool),
		leads:       NewLeadRepo(pool),
		withTenant:  withTenant(pool, cfg.TenantID),
		evoEngine:   evoEngine,
		evoClient:   evoClient,
		voice:       voice,
		rdb:         rdb,
		notifDedupe: newNotifDedupe(),
	}
}

// Handler constrói e retorna o mux HTTP com todas as rotas registradas.
func (s *Server) Handler() http.Handler {
	bp := s.cfg.BasePath
	mux := http.NewServeMux()

	// Webhook + health
	mux.HandleFunc("GET "+bp+"/webhooks/whatsapp", s.handleVerify)
	mux.HandleFunc("POST "+bp+"/webhooks/whatsapp", s.handleInbound)
	mux.HandleFunc("POST "+bp+"/webhooks/evolution", s.handleEvolutionWebhook)
	mux.HandleFunc("POST "+bp+"/webhooks/coolify", s.handleCoolifyWebhook)
	mux.HandleFunc("GET "+bp+"/health", s.handleHealth)

	// Dashboard API
	da := s.dashMiddleware
	mux.Handle("GET /api/conversations", da(s.handleDashConversations))
	mux.Handle("GET /api/conversations/{id}", da(s.handleDashGetConversation))
	mux.Handle("GET /api/conversations/{id}/messages", da(s.handleDashMessages))
	mux.Handle("PATCH /api/conversations/{id}", da(s.handleDashPatchConversation))
	mux.Handle("DELETE /api/conversations/{id}", da(s.handleDashDeleteConversation))
	mux.Handle("POST /api/conversations/{id}/messages", da(s.handleDashSendMessage))
	mux.Handle("GET /api/config", da(s.handleDashGetConfig))
	mux.Handle("GET /api/config/default-prompt", da(s.handleDashDefaultPrompt))
	mux.Handle("GET /api/config/default-admin-prompt", da(s.handleDashDefaultAdminPrompt))
	mux.Handle("PATCH /api/config", da(s.handleDashPatchConfig))
	mux.Handle("GET /api/logs", da(s.handleDashLogs))
	mux.Handle("GET /api/leads", da(s.handleDashLeads))
	mux.Handle("GET /api/bookings", da(s.handleDashBookings))
	mux.Handle("POST /api/bookings/reschedule", da(s.handleDashReschedule))
	mux.Handle("PATCH /api/leads/{id}", da(s.handleDashPatchLead))
	mux.Handle("DELETE /api/leads/{id}", da(s.handleDashDeleteLead))
	mux.Handle("GET /api/evolution/instances", da(s.handleDashEvolutionInstances))
	mux.HandleFunc("GET /api/ws", s.handleDashWS)
	// OPTIONS preflight (sem auth)
	mux.HandleFunc("OPTIONS /api/", func(w http.ResponseWriter, r *http.Request) {
		s.setCORSHeaders(w)
		w.WriteHeader(http.StatusNoContent)
	})

	return requestLogger(mux)
}

// dashMiddleware adiciona CORS e verifica X-Dash-Key.
func (s *Server) dashMiddleware(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.setCORSHeaders(w)
		if s.cfg.DashAPIKey == "" {
			http.NotFound(w, r)
			return
		}
		key := r.Header.Get("X-Dash-Key")
		if key == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				key = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.DashAPIKey)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	})
}

func (s *Server) setCORSHeaders(w http.ResponseWriter) {
	if origin := s.cfg.DashCORSOrigin; origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Dash-Key, Authorization")
	}
}

// ---------------------------------------------------------------------------
// handleVerify — GET /webhooks/whatsapp (verificação do hub Meta)
// ---------------------------------------------------------------------------

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("hub.mode")
	token := r.URL.Query().Get("hub.verify_token")
	challenge := r.URL.Query().Get("hub.challenge")

	if mode == "subscribe" && token == s.cfg.MetaWebhookVerifyToken {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(challenge))
		return
	}

	s.logger.Warn("webhook verify falhou", "mode", mode)
	http.Error(w, "Forbidden", http.StatusForbidden)
}

// ---------------------------------------------------------------------------
// handleInbound — POST /webhooks/whatsapp (mensagens recebidas)
// ---------------------------------------------------------------------------

func (s *Server) handleInbound(w http.ResponseWriter, r *http.Request) {
	// 1. Lê body (máx 4 MB)
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		s.logger.Error("erro ao ler body do webhook", "err", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// 2. Valida assinatura HMAC
	sig := r.Header.Get("X-Hub-Signature-256")
	if !ValidateMetaHMAC(body, sig, s.cfg.MetaAppSecret) {
		s.logger.Warn("assinatura HMAC inválida", "sig", sig)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 3. ACK imediato para a Meta (< 20 s)
	w.WriteHeader(http.StatusOK)

	// 4. Processa em background
	go s.processInbound(body)
}

// processInbound parseia o payload e dispara o engine para cada mensagem.
func (s *Server) processInbound(body []byte) {
	ctx := context.Background()

	msgs, err := ParseMetaWebhook(body, s.cfg.MetaPhoneNumberID)
	if err != nil {
		s.logger.Error("erro ao parsear webhook", "err", err)
		return
	}

	// Janela de debounce configurável (tenant_config.debounce_ms).
	window := s.debounceWindow(ctx)

	for _, msg := range msgs {
		// STT: transcreve nota de voz antes de tudo (Transcript + WasVoice). Best-effort.
		if msg.Content.Type == "audio" && msg.Content.Transcript == nil &&
			s.voice.Enabled() && s.sender != nil && msg.Content.MediaURL != nil {
			mediaID := strings.TrimPrefix(*msg.Content.MediaURL, "wa_media_id:")
			if audio, mime, derr := s.sender.DownloadMedia(ctx, mediaID); derr != nil {
				s.logger.Error("stt: download mídia", "err", derr)
			} else if text, terr := s.voice.Transcribe(ctx, audio, mime); terr != nil {
				s.logger.Error("stt: transcrição", "err", terr)
			} else if text != "" {
				msg.Content.Transcript = &text
				msg.WasVoice = true
			}
		}

		msg.TenantID = s.cfg.TenantID

		// Dedup via WebhookRepo
		id, isDuplicate, err := s.webhook.Record(ctx, msg.TenantID, "whatsapp", msg.ProviderMessageID, body)
		if err != nil {
			s.logger.Error("erro ao registrar webhook event", "wamid", msg.ProviderMessageID, "err", err)
			continue
		}
		if isDuplicate {
			s.logger.Debug("webhook duplicado, ignorando", "wamid", msg.ProviderMessageID)
			continue
		}

		// Acumula na rajada do número e agenda o flush (agrupa a rajada).
		// Chave prefixada pelo provider para não colidir com a Evolution (mesmo
		// telefone em canais distintos = engines distintos).
		key := "whatsapp:" + msg.ExternalID
		s.burst.add(key, burstItem{msg: msg, eventID: id})

		if window <= 0 {
			// Sem agrupamento: processa imediatamente o que estiver acumulado.
			s.flushBurst(ctx, s.engine, key)
			continue
		}
		s.dbnc.debounce(key, window, func() {
			s.flushBurst(ctx, s.engine, key)
		})
	}
}

// flushBurst processa a rajada acumulada serializando por conversa, usando o engine
// informado (Meta ou Evolution). Enquanto um Handle roda (~LLM), novas mensagens da
// mesma conversa ficam no buffer e são processadas numa única continuação ao final —
// sem respostas concorrentes.
func (s *Server) flushBurst(ctx context.Context, engine *ConversationEngine, key string) {
	items := s.burst.beginOrQueue(key)
	if items == nil {
		// Nada a fazer agora: ou já está sendo processado (as mensagens ficam
		// no buffer para o loop atual pegar), ou não há itens.
		return
	}

	for items != nil {
		combined := combineInbound(items)
		err := engine.Handle(ctx, combined)
		switch {
		case errors.Is(err, ErrMessageHeld):
			// Mensagem retida (quiet hours): NÃO marca done nem failed. O engine já
			// deixou a inbound em 'failed' + next_retry_at; o drenador de retries do
			// worker reprocessa quando vencer. Marcar done aqui a descartaria.
			s.logger.Debug("flushBurst: mensagem retida (quiet hours), aguardando retry",
				"wamid", combined.ProviderMessageID)
		case err != nil:
			s.logger.Error("engine.Handle falhou", "wamid", combined.ProviderMessageID, "err", err)
			for _, it := range items {
				if markErr := s.webhook.MarkFailed(ctx, it.eventID, err.Error()); markErr != nil {
					s.logger.Error("webhook.MarkFailed falhou", "id", it.eventID, "err", markErr)
				}
			}
		default:
			for _, it := range items {
				if markErr := s.webhook.MarkDone(ctx, it.eventID); markErr != nil {
					s.logger.Error("webhook.MarkDone falhou", "id", it.eventID, "err", markErr)
				}
			}
		}
		// Pega mensagens que chegaram durante o Handle (continua) ou libera a chave.
		items = s.burst.next(key)
	}
}

// combineInbound junta os textos das mensagens da rajada numa única inbound.
// Usa a última mensagem como base (wamid/timestamp). Se não houver texto em
// nenhuma (só mídia), mantém a última mensagem como está.
func combineInbound(items []burstItem) InboundMessage {
	base := items[len(items)-1].msg
	for _, it := range items {
		if it.msg.WasVoice {
			base.WasVoice = true
			break
		}
	}

	var parts []string
	for _, it := range items {
		if t := bestText(it.msg.Content); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) > 0 {
		base.Content = MessageContent{Type: "text", Text: strings.Join(parts, "\n")}
	}
	return base
}

// bestText extrai o melhor texto de um conteúdo (texto, transcrição ou legenda).
func bestText(c MessageContent) string {
	switch c.Type {
	case "text":
		return c.Text
	case "audio":
		if c.Transcript != nil {
			return *c.Transcript
		}
	default:
		if c.Caption != nil {
			return *c.Caption
		}
	}
	return ""
}

// debounceWindow lê a janela de agrupamento configurada (tenant_config.debounce_ms).
// Fallback de 1,5 s em erro; 0 desliga o agrupamento.
func (s *Server) debounceWindow(ctx context.Context) time.Duration {
	var ms int
	if err := s.pool.QueryRow(ctx,
		`SELECT debounce_ms FROM tenant_config WHERE tenant_id = $1`, s.cfg.TenantID,
	).Scan(&ms); err != nil {
		return 1500 * time.Millisecond
	}
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms) * time.Millisecond
}

// ---------------------------------------------------------------------------
// handleHealth — GET /health
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Evolution API — captura de leads do número não-oficial (só captura, sem responder)
// ---------------------------------------------------------------------------

type evolutionWebhook struct {
	Event    string `json:"event"`
	Instance string `json:"instance"`
	Data     struct {
		Key struct {
			RemoteJid string `json:"remoteJid"`
			FromMe    bool   `json:"fromMe"`
			ID        string `json:"id"`
			// Participant: em grupos, o JID de quem enviou (o remoteJid é o ...@g.us).
			// Em chats 1:1 vem vazio (o remetente é o próprio remoteJid).
			Participant string `json:"participant"`
			// ParticipantAlt: quando o participant vem como LID (...@lid), a Evolution
			// envia aqui o número real (...@s.whatsapp.net). Preferir este para
			// identificar o remetente (ex.: gate de admin).
			ParticipantAlt string `json:"participantAlt"`
		} `json:"key"`
		PushName string `json:"pushName"`
		Message  struct {
			Conversation        string `json:"conversation"`
			ExtendedTextMessage struct {
				Text string `json:"text"`
			} `json:"extendedTextMessage"`
		} `json:"message"`
	} `json:"data"`
}

// evolutionBotReplyEnabled lê o toggle "bot responde" (default false).
func (s *Server) evolutionBotReplyEnabled(ctx context.Context) bool {
	var on bool
	if err := s.pool.QueryRow(ctx,
		`SELECT evolution_bot_reply_enabled FROM tenant_config WHERE tenant_id = $1`, s.cfg.TenantID,
	).Scan(&on); err != nil {
		return false
	}
	return on
}

// evolutionCaptureDisabledFor diz se a captação está DESLIGADA para a instância
// (número) que recebeu a mensagem. Lista vazia / erro = captando (fail-open).
func (s *Server) evolutionCaptureDisabledFor(ctx context.Context, instance string) bool {
	if instance == "" {
		return false
	}
	var raw []byte
	if err := s.pool.QueryRow(ctx,
		`SELECT evolution_capture_disabled FROM tenant_config WHERE tenant_id = $1`, s.cfg.TenantID,
	).Scan(&raw); err != nil {
		return false // fail-open: na dúvida, captura
	}
	var disabled []string
	if err := json.Unmarshal(raw, &disabled); err != nil {
		return false
	}
	for _, n := range disabled {
		if n == instance {
			return true
		}
	}
	return false
}

// handleEvolutionWebhook recebe eventos da Evolution e captura leads (origin=evolution).
// Autenticado por segredo em ?secret=. ACK imediato; processa em background.
func (s *Server) handleEvolutionWebhook(w http.ResponseWriter, r *http.Request) {
	secret := s.cfg.EvolutionWebhookSecret
	if secret == "" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("secret")), []byte(secret)) != 1 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK) // ACK rápido para a Evolution

	var ev evolutionWebhook
	if err := json.Unmarshal(body, &ev); err != nil {
		s.logger.Debug("evolution: payload inválido", "err", err)
		return
	}

	// Interceptador de comando de operação (/status, /redeploy, /logs, /ajuda).
	// Mensagens com prefixo "/" vindas de um ADMIN acionam ações na Coolify e
	// respondem no MESMO chat (em grupo, o ...@g.us). Tratado ANTES da captura de
	// lead — inclusive em grupos, que a captura ignora. Se o comando for tratado,
	// NÃO segue para o fluxo normal (lead/IA).
	go func() {
		if s.dispatchEvolutionCommand(ev) {
			return
		}
		s.captureEvolutionLead(ev)
	}()
}

// dispatchEvolutionCommand extrai remetente/chat/texto de um evento da Evolution e
// delega ao handleAdminCommand. Retorna true se a mensagem foi consumida como
// comando (e portanto NÃO deve seguir para captura de lead / IA).
func (s *Server) dispatchEvolutionCommand(ev evolutionWebhook) bool {
	if !strings.Contains(strings.ToLower(ev.Event), "upsert") || ev.Data.Key.FromMe {
		return false
	}
	chatJid := ev.Data.Key.RemoteJid
	if chatJid == "" || strings.HasPrefix(chatJid, "status@") {
		return false
	}

	text := ev.Data.Message.Conversation
	if text == "" {
		text = ev.Data.Message.ExtendedTextMessage.Text
	}
	if !strings.HasPrefix(strings.TrimSpace(text), "/") {
		return false
	}

	// Remetente: prefere o número real (participantAlt) quando o participant vem
	// como LID; em grupo é o participant; em 1:1 é o próprio remoteJid.
	from := ev.Data.Key.ParticipantAlt
	if from == "" {
		from = ev.Data.Key.Participant
	}
	if from == "" {
		from = chatJid
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	handled, err := s.handleAdminCommand(ctx, from, chatJid, ev.Instance, text)
	if err != nil {
		s.logger.Error("evolution: comando admin falhou", "err", err)
	}
	return handled
}

// captureEvolutionLead cria/atualiza um lead a partir de uma mensagem recebida na
// Evolution. Ignora grupos, status e mensagens próprias. Não responde nada.
func (s *Server) captureEvolutionLead(ev evolutionWebhook) {
	if !strings.Contains(strings.ToLower(ev.Event), "upsert") || ev.Data.Key.FromMe {
		return
	}
	jid := ev.Data.Key.RemoteJid
	if jid == "" || strings.HasSuffix(jid, "@g.us") || strings.HasPrefix(jid, "status@") {
		return
	}
	phone := jid
	if i := strings.IndexAny(phone, "@:"); i > 0 {
		phone = phone[:i]
	}
	if phone == "" {
		return
	}

	ctx := context.Background()

	// Captação por número: se a instância que recebeu a mensagem estiver desligada,
	// ignora por completo (não cria lead nem deixa o bot responder).
	if s.evolutionCaptureDisabledFor(ctx, ev.Instance) {
		return
	}

	name := ev.Data.PushName
	text := ev.Data.Message.Conversation
	if text == "" {
		text = ev.Data.Message.ExtendedTextMessage.Text
	}

	err := s.withTenant(ctx, func(tx pgx.Tx) error {
		contact, _, err := s.contacts.FindByChannelIdentity(ctx, tx, "evolution", phone)
		if err != nil {
			return err
		}
		if contact == nil {
			contact, _, err = s.contacts.CreateWithChannelIdentity(ctx, tx, s.cfg.TenantID, "evolution", phone, name)
			if err != nil {
				return err
			}
		}
		return s.leads.CreateWithOrigin(ctx, tx, s.cfg.TenantID, contact.ID, "evolution")
	})
	if err != nil {
		s.logger.Error("evolution: falha ao capturar lead", "phone", phone, "err", err)
		return
	}
	if s.hub != nil {
		s.hub.Broadcast(WSEvent{Type: "lead.new"})
	}

	// Bot responde via Evolution SOMENTE se o toggle estiver ligado e houver texto.
	if text != "" && s.evoEngine != nil && s.evolutionBotReplyEnabled(ctx) {
		providerMsgID := ev.Data.Key.ID
		if providerMsgID == "" {
			s.logger.Debug("evolution: sem id de mensagem, ignorando resposta do bot", "phone", phone)
			return
		}

		// Dedup via WebhookRepo (provider 'evolution') — a reentrega do Baileys com o
		// mesmo id não dispara resposta dupla. Reusa o mesmo mecanismo do WhatsApp.
		rawBody, _ := json.Marshal(ev)
		id, isDuplicate, err := s.webhook.Record(ctx, s.cfg.TenantID, "evolution", providerMsgID, rawBody)
		if err != nil {
			s.logger.Error("evolution: erro ao registrar webhook event", "id", providerMsgID, "err", err)
			return
		}
		if isDuplicate {
			s.logger.Debug("evolution: webhook duplicado, ignorando", "id", providerMsgID)
			return
		}

		inbound := InboundMessage{
			TenantID:          s.cfg.TenantID,
			Channel:           "evolution",
			ExternalID:        phone,
			DisplayHandle:     name,
			ProviderMessageID: providerMsgID,
			Content:           MessageContent{Type: "text", Text: text},
			ReceivedAt:        time.Now(),
		}

		// Coalescing/debounce idêntico ao WhatsApp, com chave prefixada por canal
		// para não colidir com o mesmo telefone no número oficial.
		key := "evolution:" + phone
		s.burst.add(key, burstItem{msg: inbound, eventID: id})

		window := s.debounceWindow(ctx)
		if window <= 0 {
			s.flushBurst(ctx, s.evoEngine, key)
			return
		}
		s.dbnc.debounce(key, window, func() {
			s.flushBurst(ctx, s.evoEngine, key)
		})
	}
}
