package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// payoutDefaultLimiter é o rate-limiter global de saques (process-level).
// 1 req/10s, burst=1. Burst=1 fecha a janela de double-submit simultâneo.
// Em testes, substituímos via Server.payoutRateLimiter.
var payoutDefaultLimiter = rate.NewLimiter(rate.Every(10*time.Second), 1)

// payoutLimiterOf devolve o limiter configurado no Server ou o global padrão.
func (s *Server) payoutLimiterOf() rateLimiterIface {
	if s.payoutRateLimiter != nil {
		return s.payoutRateLimiter
	}
	return payoutDefaultLimiter
}

// withdrawalStoreOf devolve o store de saques injetado (testes) ou o store principal.
func (s *Server) withdrawalStoreOf() withdrawalStore {
	if s.payout != nil {
		return s.payout
	}
	if s.store != nil {
		return s.store
	}
	return nil
}

// tokenSudoUntilLocal extrai o sudo_exp do token de acesso presente no request.
// Espelha a lógica do handlers_sudo.go da api-go: mesma secret JWT compartilhada,
// mesmo claim sudo_exp. Retorna zero se o claim estiver ausente ou o token for inválido.
// Fail-closed: qualquer erro → time.Time{} → Before(now) → 403.
func (s *Server) tokenSudoUntilLocal(r *http.Request) time.Time {
	tok := bearerToken(r)
	if tok == "" {
		return time.Time{}
	}
	claims, err := parseClaims(tok, s.cfg.JWTSecret)
	if err != nil {
		return time.Time{}
	}
	exp, ok := claims["sudo_exp"].(float64)
	if !ok {
		return time.Time{}
	}
	// Subtrai 5s de tolerância para clock skew sem afrouxar materialmente a janela de sudo.
	return time.Unix(int64(exp), 0).Add(-5 * time.Second)
}

// payoutInput é o corpo de POST /efi/payout.
// destination é opaco — nunca logado, nunca armazenado no banco.
// idempotencyKey previne double-submit: o 2º POST com a mesma chave devolve o
// withdrawal já criado sem executar nova transferência.
type payoutInput struct {
	AmountCents    int64  `json:"amountCents"`
	Destination    string `json:"destination"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// handlePayout (POST /efi/payout) solicita um saque do saldo Efí para a conta bancária.
//
// Guards em ordem (fail-closed):
//  1. sudo_exp válido no JWT → 403 sudo_required se ausente/expirado.
//  2. Rate-limit em memória → 429 se esgotado.
//  3. amountCents > 0, destination não-vazio → 400 se inválido.
//  4. amountCents ≤ saldo disponível na Efí → 400 insufficient_balance.
//  5. PAYOUT_ENABLED=true → executa (futuro); false → 503 payout_disabled (todos
//     os guards passaram, mas nenhuma transferência é executada).
//
// NUNCA loga destination nem dados de conta bancária.
func (s *Server) handlePayout(w http.ResponseWriter, r *http.Request) {
	// ── 1. Sudo guard ────────────────────────────────────────────────────────
	if s.tokenSudoUntilLocal(r).Before(time.Now()) {
		writeError(w, http.StatusForbidden, "sudo_required", "Confirme sua identidade para esta ação")
		return
	}

	// ── 2. Rate-limit ────────────────────────────────────────────────────────
	if !s.payoutLimiterOf().Allow() {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Muitas requisições de saque. Aguarde e tente novamente.")
		return
	}

	// ── 3. Decode + validação básica ─────────────────────────────────────────
	var in payoutInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "idempotencyKey é obrigatório (ex: UUID v4)")
		return
	}
	if in.AmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_amount", "amountCents deve ser > 0")
		return
	}
	maxCents := s.cfg.PayoutMaxCents
	if maxCents <= 0 {
		maxCents = 50000
	}
	if in.AmountCents > maxCents {
		writeError(w, http.StatusBadRequest, "amount_too_large", "Valor excede o limite máximo por saque")
		return
	}
	dest := strings.TrimSpace(in.Destination)
	if dest == "" {
		writeError(w, http.StatusBadRequest, "invalid_destination", "destination é obrigatório")
		return
	}
	// dest não será mais usado abaixo — intencionalmente não logamos nem persistimos.

	// ── 4. Dedupe por idempotencyKey ─────────────────────────────────────────
	ws := s.withdrawalStoreOf()
	if ws != nil {
		if existing, err := ws.GetWithdrawalByIdempotencyKey(r.Context(), in.IdempotencyKey); err != nil {
			slog.Warn("payout: falha ao verificar idempotencyKey", "err", err)
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao verificar idempotência")
			return
		} else if existing != nil {
			writeJSON(w, http.StatusOK, existing)
			return
		}
	}

	// ── 5. Consulta saldo disponível ─────────────────────────────────────────
	if s.efi == nil {
		writeError(w, http.StatusServiceUnavailable, "efi_unavailable", "Serviço Efí indisponível")
		return
	}
	availableCents, err := s.efi.GetBalance(r.Context())
	if err != nil {
		slog.Warn("payout: falha ao consultar saldo Efí", "err", err)
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar saldo na Efí")
		return
	}
	if in.AmountCents > availableCents {
		writeError(w, http.StatusBadRequest, "insufficient_balance",
			"Valor solicitado excede o saldo disponível")
		return
	}

	// ── 5. Flag PAYOUT_ENABLED ───────────────────────────────────────────────
	// Todos os guards passaram. Se a flag estiver OFF, retornamos 503 sem executar
	// nenhuma transferência e sem registrar withdrawal "completed".
	// NUNCA logamos destination (dado bancário sensível).
	if !s.cfg.PayoutEnabled {
		slog.Info("payout: PAYOUT_ENABLED=false; guards passaram mas nenhuma transferência executada",
			"amountCents", in.AmountCents)
		writeError(w, http.StatusServiceUnavailable, "payout_disabled",
			"Saques não estão habilitados no momento. Contate o suporte.")
		return
	}

	// ── 6. Flag ON: registra e (futuro) chama a Efí ──────────────────────────
	// TODO(llms.txt): implementar chamada ao endpoint de transferência da Efí
	// quando o endpoint estiver disponível e a conta validada em produção.
	withdrawal := &Withdrawal{
		AmountCents:    in.AmountCents,
		Status:         "processing",
		PublicToken:    newPublicToken(),
		IdempotencyKey: in.IdempotencyKey,
		// Destination NÃO é gravado no banco — propositalmente omitido.
	}
	if ws != nil {
		if err := ws.CreateWithdrawal(r.Context(), withdrawal); err != nil {
			slog.Warn("payout: falha ao registrar saque", "err", err)
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar o saque")
			return
		}
	}

	writeJSON(w, http.StatusAccepted, withdrawal)
}
