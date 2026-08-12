package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

var errInstagramCommentLinkNotFound = appErr(http.StatusNotFound, "INSTAGRAM_COMMENT_LINK_NOT_FOUND", "Mapeamento não encontrado")

// verifyMetaSignature confere a assinatura HMAC-SHA256 do header
// X-Hub-Signature-256 ("sha256=<hex>") contra o app secret. Diferente do
// padrão usado no bot-go (que trata secret vazio como "pular validação", para
// dev sem credencial), aqui um secret vazio SEMPRE reprova: o caller
// (handleInstagramWebhookEvent) já responde 503 antes de chamar isso,
// então chegar aqui com secret vazio seria um bug — fail-closed por padrão
// em vez de silenciosamente aceitar qualquer payload.
func verifyMetaSignature(body []byte, signatureHeader, appSecret string) bool {
	if appSecret == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), sigBytes)
}

// GET /webhooks/instagram — handshake de verificação da assinatura do webhook
// (a Meta chama isso uma vez, ao salvar a URL do webhook no painel do app).
func (s *Server) handleInstagramWebhookVerify(w http.ResponseWriter, r *http.Request) {
	if s.cfg.InstagramWebhookVerifyToken == "" {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED", "Webhook do Instagram não configurado"))
		return
	}
	q := r.URL.Query()
	mode := q.Get("hub.mode")
	token := q.Get("hub.verify_token")
	challenge := q.Get("hub.challenge")
	if mode != "subscribe" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.InstagramWebhookVerifyToken)) != 1 {
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Verificação inválida"))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(challenge))
}

// igWebhookPayload é o envelope padrão de webhook da Meta (mesmo formato para
// WhatsApp/Instagram/Messenger): um ou mais "entry", cada um com uma ou mais
// "changes". Só o campo "comments" interessa aqui.
type igWebhookPayload struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      string `json:"id"`
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				ID   string `json:"id"` // id do comentário
				Text string `json:"text"`
				From struct {
					ID       string `json:"id"`
					Username string `json:"username"`
				} `json:"from"`
				Media struct {
					ID string `json:"id"`
				} `json:"media"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// POST /webhooks/instagram — evento de comentário novo. Responde 200 rápido
// e sempre (mesmo se o processamento interno falhar) porque a Meta reenvia
// agressivamente em caso de erro/timeout — erro de negócio (sem link
// mapeado, já respondido, etc.) não é motivo para retry.
func (s *Server) handleInstagramWebhookEvent(w http.ResponseWriter, r *http.Request) {
	if s.cfg.InstagramAppSecret == "" || !s.instagram.enabled() {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED", "Automação do Instagram não configurada"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if !verifyMetaSignature(body, r.Header.Get("X-Hub-Signature-256"), s.cfg.InstagramAppSecret) {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_SIGNATURE", "Assinatura inválida"))
		return
	}
	var payload igWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "JSON inválido"))
		return
	}
	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			if change.Field != "comments" || change.Value.ID == "" || change.Value.Media.ID == "" {
				continue
			}
			s.handleInstagramComment(r.Context(), change.Value.ID, change.Value.Media.ID, change.Value.Text)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

const instagramReplyTTL = 7 * 24 * time.Hour // janela de private reply da Meta é bem menor; sobra folga de dedupe

// handleInstagramComment resolve o link mapeado pra publicação e dispara a
// private reply. Sem link mapeado, ou com palavra-chave configurada que o
// comentário não contém: ignora silenciosamente (não é erro — a maioria dos
// comentários não deve virar resposta automática). Falhas de envio são só
// logadas (+ alerta por email): é um evento de webhook, não há requisição de
// usuário esperando resposta de erro.
func (s *Server) handleInstagramComment(ctx context.Context, commentID, mediaID, commentText string) {
	link, err := s.getInstagramCommentLinkByMediaID(ctx, mediaID)
	if err != nil {
		slog.Error("instagram webhook: falha ao buscar link mapeado", "mediaID", mediaID, "err", err)
		return
	}
	if link == nil {
		return
	}
	if link.Keyword != "" && !strings.Contains(strings.ToLower(commentText), strings.ToLower(link.Keyword)) {
		return
	}
	// SetNX atômico: garante no máximo uma private reply por comentário mesmo
	// se a Meta reenviar o mesmo evento (comportamento normal em timeout).
	acquired, err := s.rdb.SetNX(ctx, "api-go:instagram:replied:"+commentID, "1", instagramReplyTTL).Result()
	if err != nil {
		slog.Error("instagram webhook: redis indisponível, não é possível deduplicar — pulando por segurança", "commentID", commentID, "err", err)
		return
	}
	if !acquired {
		return // já respondido (ou em progresso) para este comentário
	}
	if err := s.instagram.sendPrivateReply(ctx, commentID, link.URL); err != nil {
		slog.Error("instagram webhook: falha ao enviar private reply", "commentID", commentID, "mediaID", mediaID, "err", err)
		// Libera o slot para a Meta poder reentregar o evento e tentarmos de novo.
		if delErr := s.rdb.Del(ctx, "api-go:instagram:replied:"+commentID).Err(); delErr != nil {
			slog.Warn("instagram webhook: falha ao liberar dedupe após erro de envio", "commentID", commentID, "err", delErr)
		}
		s.alertInstagramSendFailure(commentID, mediaID, link.URL, err)
	}
}

// alertInstagramSendFailure notifica por email quando uma private reply falha —
// diferente do log (que só aparece se alguém for procurar), isso avisa
// proativamente. Reaproveita SOCIAL_ALERT_EMAIL (mesmo destinatário dos
// avisos de "post em revisão"); vazio = sem alerta, só o log mesmo.
func (s *Server) alertInstagramSendFailure(commentID, mediaID, targetURL string, sendErr error) {
	if s.cfg.SocialAlertEmail == "" {
		return
	}
	body := fmt.Sprintf(`<p>A resposta automática do Instagram (comentário → DM) <strong>falhou ao enviar</strong>.</p>
<table style="border-collapse:collapse;font-family:sans-serif;font-size:14px">
<tr><td style="padding:4px 12px 4px 0;color:#666">Comentário</td><td><code>%s</code></td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#666">Publicação</td><td><code>%s</code></td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#666">Link que tentou enviar</td><td>%s</td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#666">Erro</td><td>%s</td></tr>
</table>
<p style="margin-top:16px">Causas comuns: token de acesso expirado (validade curta, ~1h se não trocado por um de longa duração) ou limite de envio da Meta.</p>`,
		html.EscapeString(commentID), html.EscapeString(mediaID), html.EscapeString(targetURL), html.EscapeString(sendErr.Error()))
	s.enqueueEmail("instagramWebhook", s.cfg.SocialAlertEmail, "Falha na automação do Instagram", body)
}

// GET /instagram/media (admin) — publicações recentes da conta conectada, pra
// alimentar o seletor visual da tela de mapeamento (em vez do admin precisar
// saber o media_id de cor).
func (s *Server) handleListInstagramMedia(w http.ResponseWriter, r *http.Request) {
	if !s.instagram.enabled() {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED", "Automação do Instagram não configurada"))
		return
	}
	media, err := s.instagram.listRecentMedia(r.Context())
	if err != nil {
		slog.Error("instagram: falha ao listar mídia recente", "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "INSTAGRAM_API_ERROR", "Falha ao consultar publicações no Instagram"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"media": media})
}

// ── CRUD admin do mapeamento post -> link ────────────────────────────────────

// GET /instagram/links (admin)
func (s *Server) handleListInstagramCommentLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.listInstagramCommentLinks(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

// PUT /instagram/links/{mediaId} (admin) — upsert idempotente.
func (s *Server) handleUpsertInstagramCommentLink(w http.ResponseWriter, r *http.Request) {
	mediaID := r.PathValue("mediaId")
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in InstagramCommentLinkInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateInstagramCommentLinkInput(mediaID, &in); err != nil {
		writeErr(w, err)
		return
	}
	link, err := s.upsertInstagramCommentLink(r.Context(), mediaID, in, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"link": link})
}

// DELETE /instagram/links/{mediaId} (admin)
func (s *Server) handleDeleteInstagramCommentLink(w http.ResponseWriter, r *http.Request) {
	mediaID := r.PathValue("mediaId")
	if err := s.deleteInstagramCommentLink(r.Context(), mediaID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
