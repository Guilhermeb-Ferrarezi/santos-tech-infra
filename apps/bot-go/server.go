package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
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
// Server
// ---------------------------------------------------------------------------

// Server encapsula as dependências do servidor HTTP.
type Server struct {
	cfg     Config
	engine  *ConversationEngine
	webhook *WebhookRepo
	logger  *slog.Logger
	dbnc    *debouncer
}

// NewServer cria um Server com as dependências fornecidas.
func NewServer(cfg Config, engine *ConversationEngine, webhook *WebhookRepo, logger *slog.Logger) *Server {
	return &Server{
		cfg:     cfg,
		engine:  engine,
		webhook: webhook,
		logger:  logger,
		dbnc:    newDebouncer(),
	}
}

// Handler constrói e retorna o mux HTTP com todas as rotas registradas.
func (s *Server) Handler() http.Handler {
	bp := s.cfg.BasePath
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+bp+"/webhooks/whatsapp", s.handleVerify)
	mux.HandleFunc("POST "+bp+"/webhooks/whatsapp", s.handleInbound)
	mux.HandleFunc("GET "+bp+"/health", s.handleHealth)
	return mux
}

// ---------------------------------------------------------------------------
// handleVerify — GET /webhooks/whatsapp (verificação do hub Meta)
// ---------------------------------------------------------------------------

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	mode      := r.URL.Query().Get("hub.mode")
	token     := r.URL.Query().Get("hub.verify_token")
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

		// Captura para o closure
		eventID := id
		captured := msg

		// Debounce de 1s por ExternalID (agrupa rajada do mesmo número)
		s.dbnc.debounce(msg.ExternalID, time.Second, func() {
			if err := s.engine.Handle(ctx, captured); err != nil {
				s.logger.Error("engine.Handle falhou", "wamid", captured.ProviderMessageID, "err", err)
				if markErr := s.webhook.MarkFailed(ctx, eventID, err.Error()); markErr != nil {
					s.logger.Error("webhook.MarkFailed falhou", "id", eventID, "err", markErr)
				}
				return
			}
			if markErr := s.webhook.MarkDone(ctx, eventID); markErr != nil {
				s.logger.Error("webhook.MarkDone falhou", "id", eventID, "err", markErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleHealth — GET /health
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
