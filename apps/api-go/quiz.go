package main

// Orquestração da resposta de questão: Jev primeiro (barato e rápido), LLM de
// texto só quando o Jev demonstra insegurança. Os dois upstreams entram por
// injeção pra esta lógica — que é onde moram as decisões — ser testável sem
// banco, sem vault e sem rede.

import (
	"context"
	"errors"
	"sort"
	"time"
)

const (
	quizSourceJev    = "jev"
	quizSourceClaude = "claude"

	// Orçamentos próprios: o API Router tem tetos largos demais pra uso
	// interativo (30s por tentativa, 60s de rotação — ver apirouter.go). O ctx
	// chega até o request do provider, então o menor prevalece. O deadline
	// curto do Jev existe pra sobrar tempo de escalar: um Jev lento não pode
	// consumir o orçamento que o fallback vai precisar.
	quizJevBudget      = 8 * time.Second
	quizFallbackBudget = 15 * time.Second
	quizTotalBudget    = 25 * time.Second

	quizMaxBodyLen = 64 << 10
)

var (
	errQuizUpstream = errors.New("quiz: nenhum modelo conseguiu responder")
	errQuizTimeout  = errors.New("quiz: tempo esgotado")
)

type quizRequest struct {
	Raw      string            `json:"raw"`
	Question string            `json:"question"`
	Options  map[string]string `json:"options"`
	Explain  bool              `json:"explain"`
}

type quizTimings struct {
	JevMs    int64 `json:"jevMs"`
	ClaudeMs int64 `json:"claudeMs"`
	TotalMs  int64 `json:"totalMs"`
}

type quizResponse struct {
	Answer        string             `json:"answer"`
	AnswerText    string             `json:"answerText"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Source        string             `json:"source"`
	Escalated     bool               `json:"escalated"`
	Degraded      bool               `json:"degraded"`
	Reasoning     string             `json:"reasoning,omitempty"`
	Parsed        quizParsed         `json:"parsed"`
	Timings       quizTimings        `json:"timings"`
}

type quizDeps struct {
	jev      func(ctx context.Context, body []byte) ([]byte, error)
	fallback func(ctx context.Context, prompt string) ([]byte, error)
	// fallbackAdapter é o ChatAdapter do provider de fallback: o parse da
	// resposta depende do formato nativo dele, e o provider vem de env.
	fallbackAdapter string
	minConfidence   float64
	minMargin       float64
}

func answerQuiz(ctx context.Context, req quizRequest, deps quizDeps) (quizResponse, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, quizTotalBudget)
	defer cancel()

	parsed, err := resolveQuizParsed(req)
	if err != nil {
		return quizResponse{}, err
	}

	resp := quizResponse{Parsed: parsed}
	verdict, jevMs, jevErr := askJev(ctx, parsed, deps)
	resp.Timings.JevMs = jevMs

	escalate := req.Explain || jevErr != nil ||
		verdict.Confidence < deps.minConfidence ||
		verdict.margin() < deps.minMargin

	if !escalate {
		fillFromJev(&resp, verdict, parsed)
		resp.Timings.TotalMs = time.Since(started).Milliseconds()
		return resp, nil
	}

	ans, claudeMs, fbErr := askFallback(ctx, parsed, deps)
	resp.Timings.ClaudeMs = claudeMs
	switch {
	case fbErr == nil:
		resp.Source = quizSourceClaude
		resp.Escalated = true
		resp.Answer = ans.Label
		resp.AnswerText = parsed.Options[ans.Label]
		resp.Reasoning = ans.Reasoning
		resp.Confidence = verdict.Confidence
		resp.Probabilities = verdict.Probabilities
	case jevErr == nil:
		// Degradação: o fallback morreu, mas o palpite do Jev existe. Devolver
		// palpite fraco é melhor que devolver erro no meio de uma questão.
		fillFromJev(&resp, verdict, parsed)
		resp.Degraded = true
	default:
		if ctx.Err() != nil {
			return quizResponse{}, errQuizTimeout
		}
		return quizResponse{}, errQuizUpstream
	}
	resp.Timings.TotalMs = time.Since(started).Milliseconds()
	return resp, nil
}

func fillFromJev(resp *quizResponse, v quizVerdict, p quizParsed) {
	resp.Source = quizSourceJev
	resp.Answer = v.Label
	resp.AnswerText = p.Options[v.Label]
	resp.Confidence = v.Confidence
	resp.Probabilities = v.Probabilities
}

// resolveQuizParsed aceita os dois formatos de entrada: bloco cru (o caminho
// normal) ou alternativas já separadas pelo cliente.
func resolveQuizParsed(req quizRequest) (quizParsed, error) {
	if len(req.Options) > 0 {
		order := make([]string, 0, len(req.Options))
		for label := range req.Options {
			order = append(order, label)
		}
		// Mapa em Go não tem ordem: sem ordenar, o prompt do fallback sairia
		// embaralhado a cada requisição e a resposta mudaria sozinha.
		sort.Strings(order)
		return quizParsed{Question: req.Question, Options: req.Options, Order: order}, nil
	}
	return parseQuizBlock(req.Raw)
}

func askJev(ctx context.Context, p quizParsed, deps quizDeps) (quizVerdict, int64, error) {
	body, err := buildJevRequest(p)
	if err != nil {
		return quizVerdict{}, 0, err
	}
	jevCtx, cancel := context.WithTimeout(ctx, quizJevBudget)
	defer cancel()
	started := time.Now()
	raw, err := deps.jev(jevCtx, body)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return quizVerdict{}, elapsed, err
	}
	v, err := parseJevResponse(raw, p)
	return v, elapsed, err
}

func askFallback(ctx context.Context, p quizParsed, deps quizDeps) (quizFallbackAnswer, int64, error) {
	fbCtx, cancel := context.WithTimeout(ctx, quizFallbackBudget)
	defer cancel()
	started := time.Now()
	raw, err := deps.fallback(fbCtx, buildFallbackPrompt(p))
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return quizFallbackAnswer{}, elapsed, err
	}
	ans, err := parseFallbackAnswer(deps.fallbackAdapter, raw, p)
	return ans, elapsed, err
}
