package main

// POST /quiz/answer — responde uma questão de múltipla escolha selecionada na
// página, para a extensão de navegador. Rota dedicada e estreita de propósito:
// a extensão NÃO alcança /auth/admin/api-router/.../proxy, que permitiria
// disparar qualquer requisição contra qualquer provider com as chaves da
// empresa.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/santos-tech/auth/db"
)

func quizErr(err error) *AppError {
	switch {
	case errors.Is(err, errQuizUnparseable):
		return appErr(http.StatusUnprocessableEntity, "UNPARSEABLE",
			"Não consegui separar as alternativas — selecione o enunciado e as alternativas")
	case errors.Is(err, errQuizTextoInsuficiente):
		return appErr(http.StatusUnprocessableEntity, "TEXTO_INSUFICIENTE",
			"Texto insuficiente para responder — selecione o enunciado completo da questão")
	case errors.Is(err, errQuizTimeout):
		return appErr(http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT", "Tempo esgotado ao consultar os modelos")
	// Mesmo código HTTP (400 INVALID_IMAGE) pras três causas — o que muda é
	// a mensagem, pra quem depura no cliente ir atrás da causa certa (mime
	// vs base64 malformado vs tamanho são problemas diferentes).
	case errors.Is(err, errQuizImagemMimeInvalido):
		return appErr(http.StatusBadRequest, "INVALID_IMAGE", "Imagem inválida — mime não suportado (use png, jpeg, webp ou gif)")
	case errors.Is(err, errQuizImagemBase64Invalido):
		return appErr(http.StatusBadRequest, "INVALID_IMAGE", "Imagem inválida — base64 malformado")
	case errors.Is(err, errQuizImagemGrandeDemais):
		return appErr(http.StatusBadRequest, "INVALID_IMAGE", "Imagem inválida — tamanho acima do limite")
	case errors.Is(err, errAPIRouterNoActiveKeys):
		return appErr(http.StatusServiceUnavailable, "NO_ACTIVE_KEYS", "Provider sem chaves ativas")
	default:
		return appErr(http.StatusBadGateway, "UPSTREAM_FAILED", "Nenhum modelo conseguiu responder")
	}
}

func (s *Server) handleQuizAnswer(w http.ResponseWriter, r *http.Request) {
	if s.apiRouterNotConfigured(w) {
		return
	}
	if s.cfg.QuizJevProviderID == 0 {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED",
			"QUIZ_JEV_PROVIDER_ID não configurado"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, quizMaxBodyLen)
	var body quizRequest
	// decodeJSONLimit, não decodeJSON: o teto padrão dele (maxJSONBody, 1MB
	// fixo em server.go) aninharia OUTRO MaxBytesReader por cima do nosso —
	// o menor prevalece, e uma imagem em base64 de alguns KB já estoura 1MB.
	// Mesmo padrão de handlers_boards.go (corpo de cena acima do padrão).
	if err := decodeJSONLimit(r, &body, quizMaxBodyLen); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "corpo inválido"))
		return
	}
	if body.Raw == "" && len(body.Options) == 0 {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "informe `raw` ou `options`"))
		return
	}

	jevProvider, err := s.q.GetAPIRouterProvider(r.Context(), s.cfg.QuizJevProviderID)
	if err != nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED", "provider do Jev não encontrado"))
		return
	}
	resp, err := answerQuiz(r.Context(), body, quizDeps{
		jev:           s.quizJevCaller(jevProvider),
		fallback:      s.quizFallbackCaller(),
		minConfidence: s.cfg.QuizMinConfidence,
		minMargin:     s.cfg.QuizMinMargin,
	})
	if err != nil {
		writeErr(w, quizErr(err))
		return
	}
	// Sem o enunciado no log: é conteúdo do usuário e não ajuda a operar.
	slog.Info("quiz: resposta",
		"source", resp.Source, "escalated", resp.Escalated, "degraded", resp.Degraded,
		"confidence", resp.Confidence, "total_ms", resp.Timings.TotalMs)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) quizJevCaller(provider db.ApiRouterProvider) func(context.Context, []byte) ([]byte, error) {
	return func(ctx context.Context, body []byte) ([]byte, error) {
		out, err := s.executeAPIRouterRequest(ctx, provider, http.MethodPost, quizJevPath, body, "", nil)
		if err != nil {
			return nil, err
		}
		if out.StatusCode >= 300 {
			return nil, fmt.Errorf("quiz: jev respondeu %d", out.StatusCode)
		}
		return out.Body, nil
	}
}

// quizFallbackCaller pede o texto ao agent-go (Claude Code em container). Sem
// provider e sem chave de API: o container roda com a assinatura da empresa,
// e é o mesmo caminho que o Pós-aula usa (agent_client.go).
//
// claudeRawCom/claudeRawImagem e não claudeRaw: o cliente padrão tem teto de
// 2 minutos, que num overlay de questão seria uma eternidade. O orçamento
// aqui é o do fallback (quizFallbackBudget/quizVisionBudget), e o ctx que
// answerQuiz passa já o limita — o timeout explícito garante que o cliente
// HTTP não fique esperando além disso se o ctx for cancelado por outro
// motivo. Sem imagem, continua em claudeRawCom (não paga o custo do caminho
// de imagem à toa); com imagem, claudeRawImagem repassa base64+mime.
func (s *Server) quizFallbackCaller() func(context.Context, string, string, string) (string, error) {
	return func(ctx context.Context, prompt, imageB64, imageMime string) (string, error) {
		if imageB64 != "" {
			return s.claudeRawImagem(ctx, prompt, imageB64, imageMime, s.cfg.QuizFallbackModel, quizVisionBudget)
		}
		return s.claudeRawCom(ctx, prompt, s.cfg.QuizFallbackModel, quizFallbackBudget)
	}
}
