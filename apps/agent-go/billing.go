package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	agentdb "github.com/santos-tech/agent/db"
)

// ── Cobrança: assinatura × chave de API ─────────────────────────────────────
//
// Por padrão todo CLI roda com o token da ASSINATURA (claude setup-token): o
// custo que o painel mostra é só simulação (preço de tabela da API), e o consumo
// desconta do limite da assinatura. O bot do WhatsApp pode ser trocado pra uma
// CHAVE DE API (custo real, limite próprio) pela tela de Uso de IA — a chave fica
// cifrada em claude_billing, igual ao token OAuth em claude_credentials.

const (
	billingSubscription = "subscription"
	billingAPIKey       = "api_key"
)

// botOrigins são as origens do bot-go — as únicas que a troca pra chave alcança.
var botOrigins = map[string]bool{"bot": true, "bot-tarefas": true}

// origemRe: a origem é um rótulo curto declarado pelo chamador ("bot",
// "posaula"...). Qualquer outra coisa é descartada (vira "") em vez de ir pro banco.
var origemRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

func normalizaOrigem(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !origemRe.MatchString(s) {
		return ""
	}
	return s
}

// credencial é a credencial com que o CLI roda numa chamada. apiKey vazio =
// assinatura (o caminho de sempre, via claudeEnv).
type credencial struct{ apiKey string }

func (c credencial) billing() string {
	if c.apiKey != "" {
		return billingAPIKey
	}
	return billingSubscription
}

// billingRow é a linha única de claude_billing, com a chave ainda cifrada.
type billingRow struct {
	BotUsesAPIKey bool
	APIKeyEnc     []byte
	Hint          string
	UpdatedAt     time.Time
}

// billingStore isola o acesso à tabela — os testes injetam um fake em memória.
type billingStore interface {
	get(ctx context.Context) (billingRow, error)
	saveKey(ctx context.Context, enc []byte, hint string) error
	clearKey(ctx context.Context) error
	// setBotUsesAPIKey devolve as linhas afetadas: 0 ao tentar ligar sem chave.
	setBotUsesAPIKey(ctx context.Context, enabled bool) (int64, error)
}

type pgBillingStore struct{ q *agentdb.Queries }

func (p pgBillingStore) get(ctx context.Context) (billingRow, error) {
	r, err := p.q.GetBilling(ctx)
	if err != nil {
		return billingRow{}, err
	}
	return billingRow{BotUsesAPIKey: r.BotUsesApiKey, APIKeyEnc: r.ApiKeyEnc, Hint: r.ApiKeyHint, UpdatedAt: r.UpdatedAt.Time}, nil
}

func (p pgBillingStore) saveKey(ctx context.Context, enc []byte, hint string) error {
	return p.q.SaveBillingAPIKey(ctx, agentdb.SaveBillingAPIKeyParams{ApiKeyEnc: enc, ApiKeyHint: hint})
}

func (p pgBillingStore) clearKey(ctx context.Context) error { return p.q.ClearBillingAPIKey(ctx) }

func (p pgBillingStore) setBotUsesAPIKey(ctx context.Context, enabled bool) (int64, error) {
	return p.q.SetBotUsesAPIKey(ctx, enabled)
}

// billingDB devolve o store injetado (testes) ou o do Postgres; nil sem banco.
func (s *Server) billingDB() billingStore {
	if s.billing != nil {
		return s.billing
	}
	if s.q == nil {
		return nil
	}
	return pgBillingStore{q: s.q}
}

// credencialPara decide com que credencial o CLI roda esta chamada. Só vai pra
// chave de API quando TUDO bate: quem chamou é serviço interno (uid 0 =
// INTERNAL_SECRET, ver authGuardAdminOrInternal), a origem é do bot, a troca está
// ligada e a chave decifra. Um admin pelo painel mandando origin "bot" continua na
// assinatura — a chave paga só é gasta pelo bot de verdade.
//
// Falha em ler/decifrar cai na assinatura com log de erro: o bot segue
// respondendo, e o evento de uso é gravado com o billing que de fato rodou.
func (s *Server) credencialPara(ctx context.Context, uid int64, origin string) credencial {
	if uid != 0 || !botOrigins[origin] {
		return credencial{}
	}
	st := s.billingDB()
	if st == nil {
		return credencial{}
	}
	row, err := st.get(ctx)
	if err != nil {
		slog.Error("cobrança: não consegui ler a troca pra chave de API; bot segue na assinatura", "err", err)
		return credencial{}
	}
	if !row.BotUsesAPIKey || len(row.APIKeyEnc) == 0 {
		return credencial{}
	}
	key, err := decrypt(s.cfg.EncryptionKey, row.APIKeyEnc)
	if err != nil || key == "" {
		slog.Error("cobrança: chave de API ilegível; bot segue na assinatura", "err", err)
		return credencial{}
	}
	return credencial{apiKey: key}
}

// ── Rotas (admin) ───────────────────────────────────────────────────────────

type billingResponse struct {
	BotUsesAPIKey    bool    `json:"botUsesApiKey"`
	APIKeyConfigured bool    `json:"apiKeyConfigured"`
	APIKeyHint       string  `json:"apiKeyHint"` // 4 últimos caracteres; a chave nunca sai daqui
	UpdatedAt        *string `json:"updatedAt"`
}

func (s *Server) writeBilling(w http.ResponseWriter, r *http.Request) {
	st := s.billingDB()
	if st == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NO_DB", "banco indisponível"))
		return
	}
	row, err := st.get(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	res := billingResponse{
		BotUsesAPIKey:    row.BotUsesAPIKey,
		APIKeyConfigured: len(row.APIKeyEnc) > 0,
		APIKeyHint:       row.Hint,
	}
	if !row.UpdatedAt.IsZero() {
		ts := row.UpdatedAt.UTC().Format(time.RFC3339)
		res.UpdatedAt = &ts
	}
	writeJSON(w, http.StatusOK, res)
}

// handleBillingGet: GET /claude/billing → estado da troca (sem a chave).
func (s *Server) handleBillingGet(w http.ResponseWriter, r *http.Request) {
	s.writeBilling(w, r)
}

// handleBillingSet: PUT /claude/billing {botUsesApiKey} — liga/desliga a troca.
func (s *Server) handleBillingSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BotUsesAPIKey *bool `json:"botUsesApiKey"`
	}
	if err := decodeJSONLimit(r, &body, 1<<10); err != nil || body.BotUsesAPIKey == nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "informe botUsesApiKey (true/false)"))
		return
	}
	st := s.billingDB()
	if st == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NO_DB", "banco indisponível"))
		return
	}
	n, err := st.setBotUsesAPIKey(r.Context(), *body.BotUsesAPIKey)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n == 0 {
		writeErr(w, appErr(http.StatusBadRequest, "API_KEY_MISSING", "cadastre a chave de API antes de ligar"))
		return
	}
	slog.Info("cobrança: troca do bot pra chave de API alterada", "ligada", *body.BotUsesAPIKey, "user", userIDFrom(r))
	s.writeBilling(w, r)
}

// apiKeyRe: formato das chaves da Anthropic (sk-ant-api...). Barra o token da
// assinatura (sk-ant-oat...) colado por engano e lixo, sem gastar uma ida à API.
var apiKeyRe = regexp.MustCompile(`^sk-ant-api[0-9A-Za-z_-]{10,}$`)

var errChaveRecusada = errors.New("chave recusada pela Anthropic")

// validarChaveAnthropic confere a chave com GET /v1/models (não gera token nem
// custo). 401/403 → errChaveRecusada; outro erro → falha transitória.
func (s *Server) validarChaveAnthropic(ctx context.Context, key string) error {
	base := strings.TrimRight(s.cfg.AnthropicAPIURL, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, base+"/v1/models?limit=1", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic inacessível: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errChaveRecusada
	default:
		return fmt.Errorf("anthropic respondeu %d", resp.StatusCode)
	}
}

// handleBillingSetKey: PUT /claude/billing/api-key {apiKey} — valida na
// Anthropic e grava cifrada. Não liga a troca sozinho (é um passo à parte).
func (s *Server) handleBillingSetKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIKey string `json:"apiKey"`
	}
	if err := decodeJSONLimit(r, &body, 1<<10); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	key := strings.TrimSpace(body.APIKey)
	if !apiKeyRe.MatchString(key) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "isso não parece uma chave de API da Anthropic (começa com sk-ant-api)"))
		return
	}
	st := s.billingDB()
	if st == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NO_DB", "banco indisponível"))
		return
	}
	if err := s.validarChaveAnthropic(r.Context(), key); err != nil {
		if errors.Is(err, errChaveRecusada) {
			writeErr(w, appErr(http.StatusBadRequest, "INVALID_API_KEY", "a Anthropic recusou esta chave"))
			return
		}
		slog.Warn("cobrança: não deu pra validar a chave na Anthropic", "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "ANTHROPIC_UNAVAILABLE", "não consegui falar com a Anthropic para validar a chave; tente de novo"))
		return
	}
	enc, err := encrypt(s.cfg.EncryptionKey, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := st.saveKey(r.Context(), enc, key[len(key)-4:]); err != nil {
		writeErr(w, err)
		return
	}
	slog.Info("cobrança: chave de API do bot gravada", "user", userIDFrom(r))
	s.writeBilling(w, r)
}

// handleBillingDeleteKey: DELETE /claude/billing/api-key — apaga a chave e
// desliga a troca (o bot volta pra assinatura na próxima mensagem).
func (s *Server) handleBillingDeleteKey(w http.ResponseWriter, r *http.Request) {
	st := s.billingDB()
	if st == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NO_DB", "banco indisponível"))
		return
	}
	if err := st.clearKey(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	slog.Info("cobrança: chave de API do bot removida", "user", userIDFrom(r))
	s.writeBilling(w, r)
}

// tstzOrNil converte o MIN(created_at) (nulo sem eventos) em string RFC3339.
func tstzOrNil(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(time.RFC3339)
	return &s
}
