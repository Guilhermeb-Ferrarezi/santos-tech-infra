package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
)

// Gerador de currículo, fase 2 — a reescrita de um campo pela IA como task do
// asynq, mesmo desenho do Pós-aula (posaula_gerar.go): POST enfileira e marca
// pending, o handler chama o agent-go e grava o resultado, GET consulta o
// estado. Diferença chave: aqui não existe "sessão" que já tem estado prévio
// pra colidir — cada pedido de reescrita é uma linha NOVA em curriculo_rewrite
// (sem TaskID fixo, sem dedup por versão), então não há conflito de fila pra
// tratar.

const (
	TaskCurriculoReescrever = "curriculo:reescrever"
	// curriculoModelo: sonnet — reescrever um parágrafo curto não precisa de
	// opus, e sonnet é uma fração do custo (a assinatura é flat, mas o balde
	// de 10 req/min do agent-go é compartilhado com o Pós-aula e o bot).
	curriculoModelo = "sonnet"
)

// curriculoTaskOpts: 1 retentativa (erro transitório de rede/5xx/429) — é uma
// reescrita de parágrafo, não vale insistir muito; timeout curto porque o
// texto de entrada é pequeno (bem menor que o brief do Pós-aula).
var curriculoTaskOpts = []asynq.Option{
	asynq.MaxRetry(1),
	asynq.Timeout(3 * time.Minute),
	asynq.Queue(posaulaAsynqQueue), // mesma fila do Pós-aula — mesmo gargalo (agent-go)
}

type curriculoReescreverPayload struct {
	RewriteID int64 `json:"rewriteId"`
}

// curriculoContextoGravado é o que vai na coluna contexto (JSONB): o campo e
// o teto de caracteres entram junto com o contexto do formulário, porque a
// task assíncrona só tem o id da linha pra reconstruir o brief inteiro depois.
type curriculoContextoGravado struct {
	Campo    string `json:"campo"`
	MaxChars int    `json:"maxChars"`
	curriculoContexto
}

// curriculoEnqueueInput é o corpo validado do POST /portal/curriculo/reescrever.
type curriculoEnqueueInput struct {
	Campo    string
	Texto    string
	MaxChars int
	Contexto curriculoContexto
}

// enqueueCurriculoReescrever grava a linha (pending) e enfileira a task. Sem
// fila (modo degradado/teste), devolve o erro — não tem fallback síncrono
// porque chamar o agent-go inline bloquearia a request por até 2 min.
func (s *Server) enqueueCurriculoReescrever(ctx context.Context, userID int64, in curriculoEnqueueInput) (int64, error) {
	if s.queue == nil {
		return 0, errFilaIndisponivel
	}
	contexto, err := json.Marshal(curriculoContextoGravado{Campo: in.Campo, MaxChars: in.MaxChars, curriculoContexto: in.Contexto})
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.portalDB.QueryRow(ctx, `INSERT INTO curriculo_rewrite (user_id, contexto, texto_original, ai_status)
		VALUES ($1, $2, $3, 'pending') RETURNING id`, userID, string(contexto), in.Texto).Scan(&id)
	if err != nil {
		return 0, err
	}
	b, err := json.Marshal(curriculoReescreverPayload{RewriteID: id})
	if err != nil {
		return 0, err
	}
	if _, err := s.queue.EnqueueContext(ctx, asynq.NewTask(TaskCurriculoReescrever, b, curriculoTaskOpts...)); err != nil {
		msg := "não consegui enfileirar a reescrita: " + err.Error()
		_ = s.curriculoSetStatus(ctx, id, "failed", &msg, nil)
		return 0, err
	}
	return id, nil
}

func (s *Server) curriculoSetStatus(ctx context.Context, id int64, status string, errMsg, textoReescrito *string) error {
	_, err := s.portalDB.Exec(ctx, `UPDATE curriculo_rewrite SET ai_status = $2, ai_error = $3, texto_reescrito = COALESCE($4, texto_reescrito) WHERE id = $1`,
		id, status, errMsg, textoReescrito)
	return err
}

// curriculoRewriteStatus é o que a tela consulta enquanto espera.
type curriculoRewriteStatus struct {
	ID             int64   `json:"id"`
	Status         string  `json:"status"`
	Error          *string `json:"error,omitempty"`
	TextoReescrito *string `json:"textoReescrito,omitempty"`
}

// curriculoBuscarStatus só devolve a linha se for do próprio usuário — a
// reescrita é self-service, ninguém vê o rascunho de reescrita de outra
// pessoa (mesmo não sendo dado muito sensível, é escopo básico de posse).
func (s *Server) curriculoBuscarStatus(ctx context.Context, userID, id int64) (*curriculoRewriteStatus, error) {
	var out curriculoRewriteStatus
	out.ID = id
	err := s.portalDB.QueryRow(ctx, `SELECT ai_status, ai_error, texto_reescrito FROM curriculo_rewrite WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&out.Status, &out.Error, &out.TextoReescrito)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// handleCurriculoReescrever é o handler asynq. Payload corrompido não vai
// melhorar com retry.
func (s *Server) handleCurriculoReescrever(ctx context.Context, t *asynq.Task) error {
	var p curriculoReescreverPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil || p.RewriteID <= 0 {
		slog.Error("curriculo: task de reescrita com payload inválido", "err", err)
		return asynq.SkipRetry
	}
	return s.curriculoReescrever(ctx, p.RewriteID)
}

// curriculoReescrever carrega a linha, monta o brief a partir do contexto
// gravado, chama o Claude (com uma retentativa se o JSON vier inválido) e
// grava o resultado.
func (s *Server) curriculoReescrever(ctx context.Context, rewriteID int64) error {
	if err := s.curriculoSetStatus(ctx, rewriteID, "running", nil, nil); err != nil {
		return fmt.Errorf("curriculo: marcar running: %w", err)
	}

	var textoOriginal, contextoRaw string
	err := s.portalDB.QueryRow(ctx, `SELECT contexto::text, texto_original FROM curriculo_rewrite WHERE id = $1`, rewriteID).
		Scan(&contextoRaw, &textoOriginal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // linha sumiu — nada a fazer
	}
	if err != nil {
		msg := "erro ao carregar o pedido: " + err.Error()
		s.curriculoSetStatus(ctx, rewriteID, "failed", &msg, nil)
		return fmt.Errorf("curriculo: carregar: %w", err)
	}
	var contexto curriculoContextoGravado
	if err := json.Unmarshal([]byte(contextoRaw), &contexto); err != nil {
		msg := "contexto inválido: " + err.Error()
		s.curriculoSetStatus(ctx, rewriteID, "failed", &msg, nil)
		return nil
	}
	maxChars := curriculoMaxCharsClamp(contexto.MaxChars)

	in := briefCurriculoInput{Campo: contexto.Campo, MaxChars: maxChars, TextoOriginal: textoOriginal, Contexto: contexto.curriculoContexto}
	brief := montarBriefCurriculoReescrita(in)
	raw, err := s.claudeRaw(ctx, brief, curriculoModelo)
	if err != nil {
		msg := err.Error()
		s.curriculoSetStatus(ctx, rewriteID, "failed", &msg, nil)
		var transitorio agentTransientError
		if errors.As(err, &transitorio) {
			return err // deixa o asynq retentar (só há 1 retentativa configurada)
		}
		return nil
	}
	out, perr := parseCurriculoRewrite(raw, maxChars)
	if perr != nil {
		in.ErroAnterior = perr.Error()
		brief = montarBriefCurriculoReescrita(in)
		raw, err = s.claudeRaw(ctx, brief, curriculoModelo)
		if err != nil {
			msg := err.Error()
			s.curriculoSetStatus(ctx, rewriteID, "failed", &msg, nil)
			return nil
		}
		out, perr = parseCurriculoRewrite(raw, maxChars)
		if perr != nil {
			msg := "resposta do Claude inválida: " + perr.Error()
			s.curriculoSetStatus(ctx, rewriteID, "failed", &msg, nil)
			return nil
		}
	}
	if err := s.curriculoSetStatus(ctx, rewriteID, "ok", nil, &out.Texto); err != nil {
		return fmt.Errorf("curriculo: gravar resultado: %w", err)
	}
	return nil
}
