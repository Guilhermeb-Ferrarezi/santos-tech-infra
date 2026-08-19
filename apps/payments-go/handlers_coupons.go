package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/time/rate"
)

// couponUniqueViolation retorna true se o erro for de violação de UNIQUE (código 23505).
func couponUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// couponStoreOf devolve s.coupons se configurado (injeção de teste), senão s.store.
func (s *Server) couponStoreOf() couponStore {
	if s.coupons != nil {
		return s.coupons
	}
	if s.store != nil {
		return s.store
	}
	return nil
}

// ── POST /coupons (admin) ─────────────────────────────────────────────────────

// createCouponInput é o corpo de POST /coupons.
type createCouponInput struct {
	Code          string `json:"code"`
	DiscountType  string `json:"discountType"`
	DiscountValue int64  `json:"discountValue"`
	MaxUses       *int64 `json:"maxUses"` // opcional; default -1 (ilimitado)
}

// handleCreateCoupon (POST /coupons) cria um cupom de desconto. Requer admin.
func (s *Server) handleCreateCoupon(w http.ResponseWriter, r *http.Request) {
	var in createCouponInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}

	in.Code = strings.TrimSpace(in.Code)
	if in.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "code é obrigatório")
		return
	}
	if in.DiscountType != "fixed" && in.DiscountType != "percent" {
		writeError(w, http.StatusBadRequest, "invalid_body", "discountType deve ser 'fixed' ou 'percent'")
		return
	}
	if in.DiscountValue <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "discountValue deve ser > 0")
		return
	}
	if in.DiscountType == "percent" && in.DiscountValue > 100 {
		writeError(w, http.StatusBadRequest, "invalid_body", "discountValue para percent deve ser entre 1 e 100")
		return
	}

	maxUses := int64(-1)
	if in.MaxUses != nil {
		maxUses = *in.MaxUses
	}

	st := s.couponStoreOf()
	if st == nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Store indisponível")
		return
	}

	c, err := st.CreateCoupon(r.Context(), Coupon{
		Code:          in.Code,
		DiscountType:  in.DiscountType,
		DiscountValue: in.DiscountValue,
		MaxUses:       maxUses,
	})
	if err != nil {
		if couponUniqueViolation(err) {
			writeError(w, http.StatusConflict, "coupon_exists", "Já existe um cupom com este código")
			return
		}
		slog.Warn("coupons: falha ao criar", "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao criar o cupom")
		return
	}

	writeJSON(w, http.StatusCreated, c)
}

// ── GET /coupons (admin) ──────────────────────────────────────────────────────

// handleListCoupons (GET /coupons) lista todos os cupons. Requer admin.
func (s *Server) handleListCoupons(w http.ResponseWriter, r *http.Request) {
	st := s.couponStoreOf()
	if st == nil {
		writeJSON(w, http.StatusOK, []Coupon{})
		return
	}
	list, err := st.ListCoupons(r.Context())
	if err != nil {
		slog.Warn("coupons: falha ao listar", "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar cupons")
		return
	}
	if list == nil {
		list = []Coupon{}
	}
	writeJSON(w, http.StatusOK, list)
}

// ── PATCH /coupons/{id} (admin) ───────────────────────────────────────────────

// patchCouponInput é o corpo de PATCH /coupons/{id}.
type patchCouponInput struct {
	Active bool `json:"active"`
}

// handlePatchCoupon (PATCH /coupons/{id}) ativa ou desativa um cupom. Requer admin.
func (s *Server) handlePatchCoupon(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "ID inválido")
		return
	}
	var in patchCouponInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}

	st := s.couponStoreOf()
	if st == nil {
		writeError(w, http.StatusNotFound, "not_found", "Cupom não encontrado")
		return
	}
	if err := st.SetCouponActive(r.Context(), id, in.Active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Cupom não encontrado")
			return
		}
		slog.Warn("coupons: falha ao alterar active", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao atualizar o cupom")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": in.Active})
}

// ── POST /coupons/apply (público) ─────────────────────────────────────────────

// applyCouponInput é o corpo de POST /coupons/apply.
type applyCouponInput struct {
	Code        string `json:"code"`
	AmountCents int64  `json:"amountCents"`
}

// applyCouponResult é a resposta de POST /coupons/apply.
type applyCouponResult struct {
	Valid         bool   `json:"valid"`
	Reason        string `json:"reason,omitempty"`
	Code          string `json:"code,omitempty"`
	DiscountType  string `json:"discountType,omitempty"`
	DiscountValue int64  `json:"discountValue,omitempty"`
	DiscountCents int64  `json:"discountCents,omitempty"`
	FinalCents    int64  `json:"finalCents,omitempty"`
}

// couponInvalid é a ÚNICA resposta negativa de /coupons/apply. Antes cada caso tinha
// sua própria reason ("cupom não encontrado", "cupom inativo", "cupom esgotado"), o
// que transformava a rota pública num oráculo: dava para varrer um dicionário de
// códigos e separar "não existe" de "existe mas está esgotado/inativo".
var couponInvalid = applyCouponResult{Valid: false, Reason: "cupom inválido"}

// applyCouponLimiters: 20 tentativas/min por IP na validação pública de cupom. Sem
// isto, /coupons/apply era enumerável à vontade — rota pública, sem autenticação e
// sem custo por tentativa.
var applyCouponLimiters = newIPLimiterRegistry(rate.Every(time.Minute/20), 20, 5*time.Minute)

// handleApplyCoupon (POST /coupons/apply) valida um cupom e calcula o desconto.
// Rota PÚBLICA — sem guard de autenticação. Não reserva uso (só valida).
// Retorna sempre 200: {valid:false,reason} ou {valid:true,...}.
func (s *Server) handleApplyCoupon(w http.ResponseWriter, r *http.Request) {
	limiter := rateLimiterIface(applyCouponLimiters.For(clientIP(r)))
	if s.couponApplyRateLimiter != nil {
		limiter = s.couponApplyRateLimiter
	}
	if !limiter.Allow() {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Muitas tentativas. Aguarde e tente novamente.")
		return
	}

	var in applyCouponInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}

	in.Code = strings.TrimSpace(in.Code)
	if in.Code == "" {
		writeJSON(w, http.StatusOK, applyCouponResult{Valid: false, Reason: "code é obrigatório"})
		return
	}
	if in.AmountCents <= 0 {
		writeJSON(w, http.StatusOK, applyCouponResult{Valid: false, Reason: "amountCents deve ser > 0"})
		return
	}

	st := s.couponStoreOf()
	if st == nil {
		writeJSON(w, http.StatusOK, couponInvalid)
		return
	}

	c, err := st.GetCouponByCode(r.Context(), in.Code)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("coupons/apply: falha ao buscar cupom", "code", in.Code, "err", err)
		}
		writeJSON(w, http.StatusOK, couponInvalid)
		return
	}

	if !c.Active {
		writeJSON(w, http.StatusOK, couponInvalid)
		return
	}
	if c.MaxUses != -1 && c.UsedCount >= c.MaxUses {
		writeJSON(w, http.StatusOK, couponInvalid)
		return
	}

	discountCents := s.couponDiscountFor(c, in.AmountCents)
	finalCents := in.AmountCents - discountCents
	if finalCents < 0 {
		finalCents = 0
	}

	writeJSON(w, http.StatusOK, applyCouponResult{
		Valid:         true,
		Code:          c.Code,
		DiscountType:  c.DiscountType,
		DiscountValue: c.DiscountValue,
		DiscountCents: discountCents,
		FinalCents:    finalCents,
	})
}

// couponDiscount calcula o desconto de um cupom sobre um valor, em centavos.
// Fonte única para o checkout do carrinho, o link de pagamento e /coupons/apply —
// antes as três cópias podiam divergir e mostrar um valor diferente do cobrado.
//
// Limites: nunca passa do valor da cobrança, e nunca passa de maxDiscountCents
// (teto absoluto; 0 ou negativo desliga). O teto existe porque um cupom percentual
// aplicado a um link de valor livre não tem limite natural — 90% de R$ 10.000 são
// R$ 9.000 de desconto.
func couponDiscount(c Coupon, amountCents, maxDiscountCents int64) int64 {
	if amountCents <= 0 {
		return 0
	}
	var d int64
	if c.DiscountType == "fixed" {
		d = c.DiscountValue
	} else {
		// percent: arredonda para o centavo mais próximo.
		d = (amountCents*c.DiscountValue + 50) / 100
	}
	if maxDiscountCents > 0 && d > maxDiscountCents {
		d = maxDiscountCents
	}
	if d > amountCents {
		d = amountCents
	}
	if d < 0 {
		d = 0
	}
	return d
}

// couponDiscountFor aplica couponDiscount com o teto configurado no servidor.
func (s *Server) couponDiscountFor(c Coupon, amountCents int64) int64 {
	return couponDiscount(c, amountCents, s.cfg.CouponMaxDiscountCents)
}

// couponAllowedOnLink diz se o cupom pode ser usado neste link. Quando o link declara
// uma lista de cupons, ela é um FILTRO, não uma sugestão visual: aceitar qualquer
// cupom ativo fazia um cupom de 90% valer num link de valor livre de R$ 10.000.
// Lista vazia = link sem restrição (comportamento antigo, preservado).
func couponAllowedOnLink(l *PaymentLink, code string) bool {
	if l == nil || len(l.Coupons) == 0 {
		return true
	}
	code = strings.ToLower(strings.TrimSpace(code))
	for _, allowed := range l.Coupons {
		if strings.ToLower(strings.TrimSpace(allowed)) == code {
			return true
		}
	}
	return false
}

// releaseCoupon devolve, best-effort, o uso reservado por RedeemCoupon quando o
// pagamento não se concretiza. Usa um contexto sem cancelamento: o request pode já
// ter sido abortado pelo cliente, e a devolução precisa acontecer mesmo assim.
func (s *Server) releaseCoupon(ctx context.Context, couponID int64) {
	if couponID <= 0 {
		return
	}
	cst := s.couponStoreOf()
	if cst == nil {
		return
	}
	if err := cst.ReleaseCouponUse(context.WithoutCancel(ctx), couponID); err != nil {
		// Não perdemos o pagamento por isso, mas o cupom fica com um uso a menos
		// disponível — precisa ser visível.
		slog.Error("falha ao devolver o uso do cupom", "coupon_id", couponID, "err", err)
	}
}
