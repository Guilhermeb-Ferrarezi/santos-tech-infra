package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
)

// Modo Rápido do gerador de currículo — orquestração + handlers HTTP. O
// aluno responde até 5 perguntas, o POST grava o pedido em curriculo_rewrite
// (kind='completo') e enfileira; o handler asynq chama o Claude UMA vez (o
// balde de 10/min do agent-go é compartilhado com o Pós-aula e o bot) e grava
// o currículo inteiro em `resultado`; o GET devolve o estado e o JSON pronto
// pro formulário do dashboard. Mesmo desenho do "Melhorar com IA"
// (curriculo_gerar.go), só que a saída é o formulário inteiro, não um campo.

const TaskCurriculoGerar = "curriculo:gerar"

type curriculoGerarPayload struct {
	RewriteID int64 `json:"rewriteId"`
}

// curriculoGerarGravado é o que vai na coluna contexto (JSONB) do pedido —
// tudo que a task precisa pra montar o brief depois, sem outra ida ao front.
type curriculoGerarGravado struct {
	Perfil    string                    `json:"perfil"`
	Respostas curriculoRespostas        `json:"respostas"`
	Contexto  curriculoCompletoContexto `json:"contexto"`
}

func (in curriculoGerarGravado) validate() error {
	if !curriculoPerfilValido(in.Perfil) {
		return validationErr("perfil inválido")
	}
	for _, r := range []string{in.Respostas.Vaga, in.Respostas.Habilidades, in.Respostas.Experiencia, in.Respostas.Projetos, in.Respostas.Estudos} {
		if len([]rune(r)) > curriculoRespostaMax {
			return validationErr("resposta muito longa")
		}
	}
	if len([]rune(in.Contexto.Nome)) > 200 {
		return validationErr("nome muito longo")
	}
	if len(in.Contexto.CursosSantosTech) > 20 {
		return validationErr("cursos demais")
	}
	return nil
}

// enqueueCurriculoGerar grava o pedido (pending) e enfileira a task. Sem
// fila (modo degradado/teste) devolve o erro — sem fallback síncrono, pelo
// mesmo motivo do reescrever: chamar o agent-go inline travaria a request.
func (s *Server) enqueueCurriculoGerar(ctx context.Context, userID int64, in curriculoGerarGravado) (int64, error) {
	if s.queue == nil {
		return 0, errFilaIndisponivel
	}
	contexto, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.portalDB.QueryRow(ctx, `INSERT INTO curriculo_rewrite (user_id, contexto, texto_original, ai_status, kind)
		VALUES ($1, $2, '', 'pending', 'completo') RETURNING id`, userID, string(contexto)).Scan(&id)
	if err != nil {
		return 0, err
	}
	b, err := json.Marshal(curriculoGerarPayload{RewriteID: id})
	if err != nil {
		return 0, err
	}
	if _, err := s.queue.EnqueueContext(ctx, asynq.NewTask(TaskCurriculoGerar, b, curriculoTaskOpts...)); err != nil {
		msg := "não consegui enfileirar a geração: " + err.Error()
		_ = s.curriculoSetResultado(ctx, id, "failed", &msg, nil)
		return 0, err
	}
	return id, nil
}

// curriculoSetResultado grava estado + (opcionalmente) o JSON do currículo.
func (s *Server) curriculoSetResultado(ctx context.Context, id int64, status string, errMsg, resultado *string) error {
	_, err := s.portalDB.Exec(ctx, `UPDATE curriculo_rewrite SET ai_status = $2, ai_error = $3, resultado = COALESCE($4::jsonb, resultado) WHERE id = $1`,
		id, status, errMsg, resultado)
	return err
}

// curriculoGerarStatus é o que a tela consulta enquanto espera; Curriculo
// só vem preenchido em status ok e é o JSON no formato do CurriculoForm.
type curriculoGerarStatus struct {
	ID        int64           `json:"id"`
	Status    string          `json:"status"`
	Error     *string         `json:"error,omitempty"`
	Curriculo json.RawMessage `json:"curriculo,omitempty"`
}

// curriculoBuscarResultado só devolve a linha se for do próprio usuário e
// do tipo certo (kind='completo') — um id de reescrita de campo não serve aqui.
func (s *Server) curriculoBuscarResultado(ctx context.Context, userID, id int64) (*curriculoGerarStatus, error) {
	var out curriculoGerarStatus
	var resultado *string
	out.ID = id
	err := s.portalDB.QueryRow(ctx, `SELECT ai_status, ai_error, resultado::text FROM curriculo_rewrite
		WHERE id = $1 AND user_id = $2 AND kind = 'completo'`, id, userID).
		Scan(&out.Status, &out.Error, &resultado)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if resultado != nil && out.Status == "ok" {
		out.Curriculo = json.RawMessage(*resultado)
	}
	return &out, nil
}

// handleCurriculoGerar é o handler asynq. Payload corrompido não melhora
// com retry.
func (s *Server) handleCurriculoGerar(ctx context.Context, t *asynq.Task) error {
	var p curriculoGerarPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil || p.RewriteID <= 0 {
		slog.Error("curriculo: task de geração com payload inválido", "err", err)
		return asynq.SkipRetry
	}
	return s.curriculoGerarCompleto(ctx, p.RewriteID)
}

// curriculoGerarCompleto carrega o pedido, monta o brief, chama o Claude
// (uma retentativa se o JSON vier inválido) e grava o currículo.
func (s *Server) curriculoGerarCompleto(ctx context.Context, id int64) error {
	if err := s.curriculoSetResultado(ctx, id, "running", nil, nil); err != nil {
		return fmt.Errorf("curriculo: marcar running: %w", err)
	}
	var contextoRaw string
	err := s.portalDB.QueryRow(ctx, `SELECT contexto::text FROM curriculo_rewrite WHERE id = $1 AND kind = 'completo'`, id).Scan(&contextoRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		msg := "erro ao carregar o pedido: " + err.Error()
		s.curriculoSetResultado(ctx, id, "failed", &msg, nil)
		return fmt.Errorf("curriculo: carregar: %w", err)
	}
	var gravado curriculoGerarGravado
	if err := json.Unmarshal([]byte(contextoRaw), &gravado); err != nil {
		msg := "pedido inválido: " + err.Error()
		s.curriculoSetResultado(ctx, id, "failed", &msg, nil)
		return nil
	}

	in := briefCurriculoCompletoInput{Perfil: gravado.Perfil, Respostas: gravado.Respostas, Contexto: gravado.Contexto}
	brief := montarBriefCurriculoCompleto(in)
	raw, err := s.claudeRaw(ctx, brief, curriculoModelo)
	if err != nil {
		msg := curriculoMensagemDeErro(err)
		s.curriculoSetResultado(ctx, id, "failed", &msg, nil)
		var transitorio agentTransientError
		if errors.As(err, &transitorio) {
			return err // asynq retenta (MaxRetry 1)
		}
		return nil
	}
	out, perr := parseCurriculoCompleto(raw, gravado.Perfil)
	if perr != nil {
		slog.Warn("curriculo: geração completa inválida, pedindo de novo", "id", id, "err", perr)
		in.ErroAnterior = perr.Error()
		brief = montarBriefCurriculoCompleto(in)
		raw, err = s.claudeRaw(ctx, brief, curriculoModelo)
		if err != nil {
			msg := curriculoMensagemDeErro(err)
			s.curriculoSetResultado(ctx, id, "failed", &msg, nil)
			return nil
		}
		out, perr = parseCurriculoCompleto(raw, gravado.Perfil)
		if perr != nil {
			msg := "resposta do Claude inválida: " + perr.Error()
			s.curriculoSetResultado(ctx, id, "failed", &msg, nil)
			return nil
		}
	}
	resultado, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("curriculo: serializar resultado: %w", err)
	}
	res := string(resultado)
	if err := s.curriculoSetResultado(ctx, id, "ok", nil, &res); err != nil {
		return fmt.Errorf("curriculo: gravar resultado: %w", err)
	}
	return nil
}

// curriculoMensagemDeErro traduz o erro do agent-go pro que a tela mostra:
// o 429 (balde de 10/min compartilhado) vira um aviso que o aluno entende;
// o resto vai como está (já é legível — ver agent_client.go).
func curriculoMensagemDeErro(err error) string {
	if strings.Contains(err.Error(), "status 429") {
		return "Muita gente usando a IA agora — tenta de novo em 1 minuto"
	}
	return err.Error()
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

// handleCurriculoPostGerar (POST /portal/curriculo/gerar) body
// {perfil, respostas{vaga,habilidades,experiencia,projetos,estudos}, contexto{nome,cursosSantosTech[]}}
// → 202 {id, queued:true}. Tudo opcional menos perfil.
func (s *Server) handleCurriculoPostGerar(w http.ResponseWriter, r *http.Request) {
	var in curriculoGerarGravado
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	id, err := s.enqueueCurriculoGerar(r.Context(), userIDFrom(r), in)
	if err != nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", "Não consegui montar o currículo agora — tente de novo em instantes"))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "queued": true})
}

// handleCurriculoGetGerar (GET /portal/curriculo/gerar/{id}) →
// {id, status, error?, curriculo?}. 404 se não é do usuário.
func (s *Server) handleCurriculoGetGerar(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.curriculoBuscarResultado(r.Context(), userIDFrom(r), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if out == nil {
		writeErr(w, notFoundErr("Pedido de currículo"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
