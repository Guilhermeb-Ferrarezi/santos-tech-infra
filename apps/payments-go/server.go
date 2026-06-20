package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/santos-tech/golog"
)

type Server struct {
	cfg       Config
	db        *pgxpool.Pool
	rdb       *redis.Client
	store     *Store
	analytics analyticsSource
	charges   chargeReader
	recs      recurrenceStore
	subs      subscribeStore
	cart      *CartStore
	provider  PaymentProvider
	efi       efiOps
	email     *emailClient
	// queue enfileira tasks asynq (notificação de pagamento). Pode ser nil em
	// testes que não montam a fila — os callers tratam o nil com fallback.
	queue *asynq.Client
}

func NewServer(cfg Config, db *pgxpool.Pool, rdb *redis.Client, provider PaymentProvider) *Server {
	st := newStore(db)
	s := &Server{
		cfg:       cfg,
		db:        db,
		rdb:       rdb,
		store:     st,
		analytics: st,
		charges:   st,
		recs:      st,
		subs:      st,
		cart:      &CartStore{rdb: rdb},
		provider:  provider,
		email:     newEmailClient(cfg),
	}
	// O *efiProvider satisfaz tanto PaymentProvider quanto efiOps.
	if ep, ok := provider.(*efiProvider); ok {
		s.efi = ep
	}
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", s.handleReady)  // público: pinga Postgres+Redis
	mux.Handle("GET /metrics", metricsHandler()) // público: Prometheus

	mux.HandleFunc("POST /students", s.requireAdmin(s.handleCreateStudent))
	mux.HandleFunc("GET /students", s.requireAdmin(s.handleListStudents))
	mux.HandleFunc("GET /students/{id}", s.requireAdmin(s.handleGetStudent))
	mux.HandleFunc("GET /students/{id}/charges", s.requireAdmin(s.handleStudentCharges))

	mux.HandleFunc("POST /plans", s.requireAdmin(s.handleCreatePlan))
	mux.HandleFunc("GET /plans", s.requireAdmin(s.handleListPlans))

	mux.HandleFunc("POST /subscriptions", s.requireAdmin(s.handleCreateSubscription))
	mux.HandleFunc("GET /subscriptions", s.requireAdmin(s.handleListSubscriptions))
	mux.HandleFunc("PATCH /subscriptions/{id}", s.requireAdmin(s.handlePatchSubscription))

	mux.HandleFunc("GET /stats", s.requireAdmin(s.handleStats))
	mux.HandleFunc("GET /analytics", s.requireAdmin(s.handleAnalytics))
	mux.HandleFunc("POST /charges", s.requireAdmin(s.handleCreateCharge))
	mux.HandleFunc("GET /charges", s.requireAdmin(s.handleListCharges))
	mux.HandleFunc("GET /charges/{id}", s.requireAdmin(s.handleGetCharge))

	mux.HandleFunc("GET /customers", s.requireAdmin(s.handleListCustomers))
	mux.HandleFunc("GET /customers/{id}", s.requireAdmin(s.handleGetCustomer))

	mux.HandleFunc("POST /recurrences", s.requireAdmin(s.handleCreateRecurrence))
	mux.HandleFunc("GET /recurrences", s.requireAdmin(s.handleListRecurrences))
	mux.HandleFunc("GET /recurrences/{id}", s.requireAdmin(s.handleGetRecurrence))
	mux.HandleFunc("POST /recurrences/{id}/cancel", s.requireAdmin(s.handleCancelRecurrence))

	mux.HandleFunc("POST /products", s.requireAdmin(s.handleCreateProduct))
	mux.HandleFunc("GET /products", s.requireAdmin(s.handleListProducts))
	mux.HandleFunc("PUT /products/{id}", s.requireAdmin(s.handleUpdateProduct))
	mux.HandleFunc("DELETE /products/{id}", s.requireAdmin(s.handleDeleteProduct))
	mux.HandleFunc("GET /products/by-slug/{slug}", s.handleGetProductBySlug) // público

	mux.HandleFunc("GET /me/customer", s.authGuard(s.handleGetMeCustomer))
	mux.HandleFunc("PUT /me/customer", s.authGuard(s.handlePutMeCustomer))
	mux.HandleFunc("GET /me/cart", s.authGuard(s.handleGetCart))
	mux.HandleFunc("POST /me/cart", s.authGuard(s.handleAddCart))
	mux.HandleFunc("DELETE /me/cart/{productId}", s.authGuard(s.handleRemoveCart))
	mux.HandleFunc("POST /me/cart/checkout", s.authGuard(s.handleCheckout))
	mux.HandleFunc("POST /me/subscribe", s.authGuard(s.handleSubscribe))
	mux.HandleFunc("GET /me/charges", s.authGuard(s.handleMeCharges))

	mux.HandleFunc("GET /pay/{token}", s.handleGetPay)
	mux.HandleFunc("GET /pay/{token}/events", s.handlePayEvents)
	mux.HandleFunc("POST /pay/{token}/cancel", s.handleCancelPay)
	mux.HandleFunc("GET /pay/{token}/receipt", s.handlePayReceipt)
	// Status da assinatura (público, token): a tela de checkout acompanha a autorização.
	mux.HandleFunc("GET /subscribe/{token}", s.handleGetSubscribe)
	mux.HandleFunc("GET /subscribe/{token}/events", s.handleSubscribeEvents)

	// A Efí valida a URL base no registro (POST esperando 200) e anexa "/pix" só
	// nas notificações reais. O segredo vai como ?token= (param único, redigido no
	// log) e sobrevive ao append do /pix. Registramos ambas as rotas no mesmo handler.
	mux.HandleFunc("POST /webhooks/efi", s.handleWebhook)
	mux.HandleFunc("POST /webhooks/efi/pix", s.handleWebhook)

	// Webhook de recorrências (mudança de status do contrato PIX Automático), separado
	// do webhook pix do débito. Mesmo esquema de segredo em ?token= (a Efí pode anexar
	// um sufixo de rota ao final, tratado no handler).
	mux.HandleFunc("POST /webhooks/efi/rec", s.handleRecWebhook)

	mux.HandleFunc("GET /efi/balance", s.requireAdmin(s.handleEfiBalance))
	mux.HandleFunc("GET /efi/med", s.requireAdmin(s.handleEfiMED))
	mux.HandleFunc("GET /charges/{id}/receipt", s.requireAdmin(s.handleReceipt))
	mux.HandleFunc("POST /efi/reports", s.requireAdmin(s.handleReportRequest))
	mux.HandleFunc("GET /efi/reports/{id}", s.requireAdmin(s.handleReportGet))

	return golog.RequestLogger(s.cors(metricsMiddleware(mux)))
}

// handleReady verifica as dependências (Postgres e Redis) com timeout curto.
// Responde 503 se alguma falhar — usado pelo readiness probe.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if s.db != nil {
		if err := s.db.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "dependency": "postgres"})
			return
		}
	}
	if s.rdb != nil {
		if err := s.rdb.Ping(ctx).Err(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "dependency": "redis"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		for _, o := range s.cfg.CORSOrigins {
			if o == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				break
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Auth ──────────────────────────────────────────────────────────────────

type ctxKey string

const userIDKey ctxKey = "uid"

// bearerToken extrai o token do cookie access_token ou do header Authorization: Bearer.
func bearerToken(r *http.Request) string {
	if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
		return c.Value
	}
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return after
	}
	return ""
}

func (s *Server) authGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Não autenticado")
			return
		}
		uid, err := verifyToken(tok, s.cfg.JWTSecret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Não autenticado")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	}
}

// requireAdmin exige token válido E role Admin (users.role == 3) no Postgres compartilhado.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		uid := r.Context().Value(userIDKey).(int64)
		var role int
		err := s.db.QueryRow(r.Context(), `SELECT role FROM users WHERE id=$1`, uid).Scan(&role)
		if err != nil || role != 3 {
			writeError(w, http.StatusForbidden, "forbidden", "Acesso restrito a administradores")
			return
		}
		next(w, r)
	})
}
