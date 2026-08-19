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

// pixWebhookStore define as operações de persistência usadas pelo handleWebhook (PIX).
// Extraída como interface para facilitar testes sem DB.
type pixWebhookStore interface {
	// ClaimWebhookEvent/MarkWebhookDone formam o processamento em duas fases:
	// reivindica ('pending') → aplica o efeito → encerra ('done'). Nunca encerre
	// antes do efeito: a reentrega da Efí precisa reprocessar quando algo falha.
	ClaimWebhookEvent(ctx context.Context, id, typ string, payload []byte) (bool, error)
	MarkWebhookDone(ctx context.Context, id string) error
	MarkChargePaid(ctx context.Context, correlationID string) error
	PublicTokenByCorrelation(ctx context.Context, correlationID string) (string, error)
	// SetWithdrawalStatus aplica o resultado final de um Pix Envio (payout), casando o
	// saque por efi_id_envio OU e2e_id. Usado pelo ramo PAYOUT_RESULT do webhook pix.
	SetWithdrawalStatus(ctx context.Context, idEnvio, e2eID, status string) error
}

// cobrWebhookStore define as operações usadas pelo handleCobrWebhook (API Cobranças:
// boleto/cartão). Mesmo contrato de duas fases do webhook pix.
type cobrWebhookStore interface {
	ClaimWebhookEvent(ctx context.Context, id, typ string, payload []byte) (bool, error)
	MarkWebhookDone(ctx context.Context, id string) error
	MarkChargePaidByProviderID(ctx context.Context, providerChargeID string) (bool, error)
	PublicTokenByProviderID(ctx context.Context, providerChargeID string) (string, error)
}

// productStore define as operações de persistência de produtos usadas pelos
// handlers de /products. Extraída como interface para facilitar testes sem DB.
type productStore interface {
	CreateProduct(ctx context.Context, p *Product) error
	ListProducts(ctx context.Context) ([]Product, error)
	UpdateProduct(ctx context.Context, p *Product) error
	DeleteProduct(ctx context.Context, id int64) error
	GetProductByID(ctx context.Context, id int64) (*Product, error)
	GetProductBySlug(ctx context.Context, slug string) (*Product, error)
}

// couponStore define as operações de persistência de cupons.
// Extraída como interface para facilitar testes sem DB.
type couponStore interface {
	CreateCoupon(ctx context.Context, c Coupon) (Coupon, error)
	ListCoupons(ctx context.Context) ([]Coupon, error)
	GetCouponByCode(ctx context.Context, code string) (Coupon, error)
	SetCouponActive(ctx context.Context, id int64, active bool) error
	IncrementCouponUse(ctx context.Context, id int64) error
}

// checkoutStore isola o acesso ao banco usado pelo checkout do carrinho (POST
// /me/cart/checkout): produto e cliente. O *Store satisfaz em prod; um fake nos
// testes (sem Postgres). A criação da cobrança usa createAndPersistCharge que
// por sua vez precisa de store + provider.
type checkoutStore interface {
	GetProductByID(ctx context.Context, id int64) (*Product, error)
	UpsertCustomer(ctx context.Context, userID int64, taxID, phone, name, email string) (*Customer, error)
	InsertChargeItems(ctx context.Context, chargeID int64, items []ChargeItem) error
}

// paymentLinkStore define as operações de persistência de links de pagamento.
// Extraída como interface para facilitar testes sem DB.
type paymentLinkStore interface {
	CreatePaymentLink(ctx context.Context, l *PaymentLink) error
	GetPaymentLinkByToken(ctx context.Context, token string) (*PaymentLink, error)
	GetPaymentLink(ctx context.Context, id int64) (*PaymentLink, error)
	ListPaymentLinks(ctx context.Context) ([]PaymentLink, error)
	SetPaymentLinkStatus(ctx context.Context, id int64, status string) error
	InsertChargeWithLink(ctx context.Context, c *Charge, linkID int64) error
	// MarkChargePaid é necessário para marcar cobrança paga após cartão sincronamente.
	MarkChargePaid(ctx context.Context, correlationID string) error
}

// rateLimiterIface permite injetar limiters alternativos nos testes (sem depender
// da variável global). Em produção, usamos *rate.Limiter diretamente.
type rateLimiterIface interface {
	Allow() bool
}

type Server struct {
	cfg        Config
	db         *pgxpool.Pool
	rdb        *redis.Client
	store      *Store
	pixWH      pixWebhookStore  // store do webhook PIX; nil usa s.store
	cobrWH     cobrWebhookStore // store do webhook Cobranças (boleto/cartão); nil usa s.store
	links      paymentLinkStore // store de links; nil usa s.store
	coupons    couponStore      // store de cupons; nil usa s.store
	products   productStore     // store de produtos; nil usa s.store
	checkoutSt checkoutStore    // store do checkout do carrinho; nil usa s.store
	payout     withdrawalStore  // store de saques; nil usa s.store
	analytics  analyticsSource
	charges    chargeReader
	recs       recurrenceStore
	subs       subscribeStore
	cart       *CartStore
	provider   PaymentProvider
	efi        efiOps
	efiCobr    *efiCobrancas // API Cobranças (boleto); nil se EFI_COBR_BASE_URL ausente
	email      *emailClient
	// refund store de estorno de cartão; nil usa s.store.
	refund chargeRefundStore
	// payoutRateLimiter pode ser substituído em testes; nil usa o global payoutDefaultLimiter.
	payoutRateLimiter rateLimiterIface
	// linkPayRateLimiter substitui o rate-limit por IP de /link/{token}/pay em testes;
	// nil usa o limitador global por IP (payLinkLimiterFor).
	linkPayRateLimiter rateLimiterIface
	// statement store de extrato; nil usa s.store.
	statement statementStore
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
	// API Cobranças (boleto) só sobe se a base estiver configurada.
	if cfg.EFICobrBaseURL != "" {
		s.efiCobr = newEfiCobrancas(cfg)
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

	mux.HandleFunc("POST /internal/gerar-cobrancas", s.requireAdmin(s.handleInternalGenerateCharges))
	mux.HandleFunc("GET /stats", s.requireAdmin(s.handleStats))
	mux.HandleFunc("GET /analytics", s.requireAdmin(s.handleAnalytics))
	mux.HandleFunc("POST /charges", s.requireAdmin(s.handleCreateCharge))
	mux.HandleFunc("POST /charges/manual", s.requireAdmin(s.handleCreateManualCharge))
	mux.HandleFunc("GET /charges", s.requireAdmin(s.handleListCharges))
	mux.HandleFunc("GET /charges/{id}", s.requireAdmin(s.handleGetCharge))

	mux.HandleFunc("GET /customers", s.requireAdmin(s.handleListCustomers))
	mux.HandleFunc("GET /customers/{id}", s.requireAdmin(s.handleGetCustomer))

	mux.HandleFunc("POST /recurrences", s.requireAdmin(s.handleCreateRecurrence))
	mux.HandleFunc("POST /recurrences/manual", s.requireAdmin(s.handleCreateManualRecurrence))
	mux.HandleFunc("GET /recurrences", s.requireAdmin(s.handleListRecurrences))
	mux.HandleFunc("GET /recurrences/{id}", s.requireAdmin(s.handleGetRecurrence))
	mux.HandleFunc("POST /recurrences/{id}/cancel", s.requireAdmin(s.handleCancelRecurrence))

	mux.HandleFunc("POST /products", s.requireAdmin(s.handleCreateProduct))
	mux.HandleFunc("GET /products", s.requireAdmin(s.handleListProducts))
	mux.HandleFunc("GET /products/{id}", s.requireAdmin(s.handleGetProduct))
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
	mux.HandleFunc("GET /me/recurrences", s.authGuard(s.handleMeRecurrences))

	mux.HandleFunc("GET /pay/{token}", s.handleGetPay)
	mux.HandleFunc("GET /pay/{token}/qr.png", s.handlePayQR) // PNG do QR p/ embutir no e-mail
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

	// Webhook da API Cobranças (boleto): a Efí POSTa {"notification":"<token>"} a cada
	// mudança de status; o handler busca o detalhe e marca a cobrança paga pelo charge_id.
	mux.HandleFunc("POST /webhooks/efi/cobr", s.handleCobrWebhook)

	// Cartão de crédito (API Cobranças Efí — Basic Auth, sem mTLS).
	// POST /charges/card: recebe somente payment_token + dados não-sensíveis (sem PAN/CVV).
	mux.HandleFunc("POST /charges/card", s.requireAdmin(s.handleCreateCardCharge))
	// GET /installments: parcelas por bandeira e valor (admin).
	mux.HandleFunc("GET /installments", s.requireAdmin(s.handleGetInstallments))
	// POST /charges/card/{id}/refund: estorno (admin).
	mux.HandleFunc("POST /charges/card/{id}/refund", s.requireAdmin(s.handleRefundCard))

	mux.HandleFunc("GET /efi/balance", s.requireAdmin(s.handleEfiBalance))
	mux.HandleFunc("GET /efi/med", s.requireAdmin(s.handleEfiMED))
	mux.HandleFunc("GET /charges/{id}/receipt", s.requireAdmin(s.handleReceipt))
	mux.HandleFunc("POST /efi/reports", s.requireAdmin(s.handleReportRequest))
	mux.HandleFunc("GET /efi/reports/{id}", s.requireAdmin(s.handleReportGet))

	// Saques (payout): admin-only + sudo + rate-limit + flag PAYOUT_ENABLED.
	mux.HandleFunc("POST /efi/payout", s.requireAdmin(s.handlePayout))
	mux.HandleFunc("GET /withdrawals", s.requireAdmin(s.handleListWithdrawals))

	// Links de pagamento (admin).
	mux.HandleFunc("POST /payment-links", s.requireAdmin(s.handleCreatePaymentLink))
	mux.HandleFunc("GET /payment-links", s.requireAdmin(s.handleListPaymentLinks))
	mux.HandleFunc("GET /payment-links/{id}", s.requireAdmin(s.handleGetPaymentLink))
	mux.HandleFunc("PATCH /payment-links/{id}", s.requireAdmin(s.handlePatchPaymentLink))

	// Link público (checkout): resolve dados e aceita pagamento.
	mux.HandleFunc("GET /link/{token}", s.handleGetLinkByToken)
	mux.HandleFunc("POST /link/{token}/pay", s.handlePayViaLink)

	// Cupons de desconto.
	mux.HandleFunc("POST /coupons", s.requireAdmin(s.handleCreateCoupon))
	mux.HandleFunc("GET /coupons", s.requireAdmin(s.handleListCoupons))
	mux.HandleFunc("PATCH /coupons/{id}", s.requireAdmin(s.handlePatchCoupon))
	// /coupons/apply é PÚBLICO — usado pelo checkout para validar o cupom.
	mux.HandleFunc("POST /coupons/apply", s.handleApplyCoupon)

	// Extrato de movimentos (admin): entradas (cobranças pagas) + saídas (saques).
	// O CSV de conciliação Efí (GET /efi/reports/*) permanece inalterado.
	mux.HandleFunc("GET /statement", s.requireAdmin(s.handleStatement))

	return golog.RequestLogger(s.cors(s.ipBanCheck(metricsMiddleware(mux))))
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
		golog.SetUserID(r.Context(), uid)
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	}
}

// requireAdmin exige token válido E role Admin (users.role == 3) no Postgres compartilhado.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		uid := r.Context().Value(userIDKey).(int64)
		var role int
		var name string
		err := s.db.QueryRow(r.Context(), `SELECT role, name FROM users WHERE id=$1`, uid).Scan(&role, &name)
		if err != nil || role != 3 {
			writeError(w, http.StatusForbidden, "forbidden", "Acesso restrito a administradores")
			return
		}
		golog.SetUserName(r.Context(), name)
		next(w, r)
	})
}
