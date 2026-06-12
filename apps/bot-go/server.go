package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
// burstBuffer — acumula as mensagens de uma rajada por chave (ExternalID) para
// que sejam combinadas em um único Handle quando o debounce dispara. Sem isso,
// só a última mensagem da rajada seria processada (as anteriores se perdiam).
// ---------------------------------------------------------------------------

type burstItem struct {
	msg     InboundMessage
	eventID WebhookEventID
}

type burstBuffer struct {
	mu    sync.Mutex
	items map[string][]burstItem
}

func newBurstBuffer() *burstBuffer { return &burstBuffer{items: make(map[string][]burstItem)} }

func (b *burstBuffer) add(key string, it burstItem) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items[key] = append(b.items[key], it)
}

func (b *burstBuffer) take(key string) []burstItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.items[key]
	delete(b.items, key)
	return out
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
}

// NewServer cria um Server com as dependências fornecidas.
func NewServer(cfg Config, engine *ConversationEngine, webhook *WebhookRepo, pool *pgxpool.Pool, sender *WhatsAppSender, logger *slog.Logger, hub *WSHub, logRepo *ProcessingLogRepo) *Server {
	return &Server{
		cfg:     cfg,
		engine:  engine,
		webhook: webhook,
		pool:    pool,
		sender:  sender,
		logger:  logger,
		dbnc:    newDebouncer(),
		burst:   newBurstBuffer(),
		hub:     hub,
		logRepo: logRepo,
	}
}

// Handler constrói e retorna o mux HTTP com todas as rotas registradas.
func (s *Server) Handler() http.Handler {
	bp := s.cfg.BasePath
	mux := http.NewServeMux()

	// Webhook + health
	mux.HandleFunc("GET "+bp+"/webhooks/whatsapp", s.handleVerify)
	mux.HandleFunc("POST "+bp+"/webhooks/whatsapp", s.handleInbound)
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
	mux.Handle("PATCH /api/config", da(s.handleDashPatchConfig))
	mux.Handle("GET /api/logs", da(s.handleDashLogs))
	mux.HandleFunc("GET /api/ws", s.handleDashWS)
	// OPTIONS preflight (sem auth)
	mux.HandleFunc("OPTIONS /api/", func(w http.ResponseWriter, r *http.Request) {
		s.setCORSHeaders(w)
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
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
		key := msg.ExternalID
		s.burst.add(key, burstItem{msg: msg, eventID: id})

		if window <= 0 {
			// Sem agrupamento: processa imediatamente o que estiver acumulado.
			s.flushBurst(ctx, key)
			continue
		}
		s.dbnc.debounce(key, window, func() {
			s.flushBurst(ctx, key)
		})
	}
}

// flushBurst combina as mensagens acumuladas de uma rajada em um único Handle.
func (s *Server) flushBurst(ctx context.Context, key string) {
	items := s.burst.take(key)
	if len(items) == 0 {
		return
	}

	combined := combineInbound(items)
	if err := s.engine.Handle(ctx, combined); err != nil {
		s.logger.Error("engine.Handle falhou", "wamid", combined.ProviderMessageID, "err", err)
		for _, it := range items {
			if markErr := s.webhook.MarkFailed(ctx, it.eventID, err.Error()); markErr != nil {
				s.logger.Error("webhook.MarkFailed falhou", "id", it.eventID, "err", markErr)
			}
		}
		return
	}
	for _, it := range items {
		if markErr := s.webhook.MarkDone(ctx, it.eventID); markErr != nil {
			s.logger.Error("webhook.MarkDone falhou", "id", it.eventID, "err", markErr)
		}
	}
}

// combineInbound junta os textos das mensagens da rajada numa única inbound.
// Usa a última mensagem como base (wamid/timestamp). Se não houver texto em
// nenhuma (só mídia), mantém a última mensagem como está.
func combineInbound(items []burstItem) InboundMessage {
	base := items[len(items)-1].msg

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
