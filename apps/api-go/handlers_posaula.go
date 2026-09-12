package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Handlers do Pós-aula (práticas geradas pelo Claude — ver posaula_*.go).
// Aluno: lista e responde as PRÓPRIAS práticas (só authGuard; o escopo é o
// e-mail da sessão, casado com o "user" do Portal). Professor: vê, exclui e
// pede de novo as práticas de uma aula (guard portalDiario, o mesmo do
// diário — quem registra a aula é quem cuida do que saiu dela).

// portalUserFromSession resolve o usuário da sessão (auth central). nil com
// 401 já escrito quando não dá — o padrão dos handlers self-service.
func (s *Server) portalUserFromSession(w http.ResponseWriter, r *http.Request) *User {
	u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return nil
	}
	if u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado"))
		return nil
	}
	return u
}

// handlePortalMyTasks (GET /portal/me/tasks?status=pending|done&classId=&limit=)
// → {tasks}. Só práticas já liberadas (07:00 do dia seguinte à aula) e não
// excluídas, as `limit` mais recentes (default 50, máx 200). Quem não existe
// no Portal recebe lista vazia, não erro.
func (s *Server) handlePortalMyTasks(w http.ResponseWriter, r *http.Request) {
	status, err := posaulaStatusFrom(r.URL.Query().Get("status"))
	if err != nil {
		writeErr(w, err)
		return
	}
	classID, err := posaulaOptionalQueryID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	limit, err := posaulaLimitFrom(r.URL.Query().Get("limit"))
	if err != nil {
		writeErr(w, err)
		return
	}
	u := s.portalUserFromSession(w, r)
	if u == nil {
		return
	}
	userID, ok, err := s.posaulaPortalUserID(r.Context(), u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"tasks": []posaulaPraticaDTO{}})
		return
	}
	tasks, err := s.posaulaMinhasPraticas(r.Context(), userID, status, classID, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

// handlePortalAnswerTask (POST /portal/me/tasks/{taskId}/answer) body
// {selectedOption?, answerText?} → 200 {answer}. Múltipla escolha volta
// corrigida na hora; aberta volta sem veredito e a correção pelo Claude
// entra na fila. 404 se a prática não é do aluno ou ainda não liberou;
// 409 se já respondida.
func (s *Server) handlePortalAnswerTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := portalPathID(r, "taskId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in posaulaRespostaInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	u := s.portalUserFromSession(w, r)
	if u == nil {
		return
	}
	userID, ok, err := s.posaulaPortalUserID(r.Context(), u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, notFoundErr("Prática"))
		return
	}
	p, err := s.posaulaCarregarPraticaDoAluno(r.Context(), userID, taskID)
	if err != nil {
		writeErr(w, err)
		return
	}
	answer, answerID, err := s.posaulaResponder(r.Context(), userID, p, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if p.Kind == "aberta" {
		// Sem fila não tem quem corrija: em vez de deixar o aluno esperando
		// um feedback que nunca vem, já cai no "o professor vai olhar".
		if err := s.enqueuePosaulaCorrigir(r.Context(), answerID); err != nil {
			slog.Warn("posaula: não consegui enfileirar a correção, caindo no fallback", "answer", answerID, "err", err)
			if err := s.posaulaMarcarCorrecaoFalhou(r.Context(), answerID, nil); err == nil {
				fb := posaulaFeedbackFalhou
				answer.Feedback = &fb
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer})
}

// handlePortalSessionTasks (GET /portal/sessions/{sessionId}/tasks) →
// {tasks, aiStatus, aiError}. A tela do professor consulta isto a cada 5s
// enquanto aiStatus for pending/running.
func (s *Server) handlePortalSessionTasks(w http.ResponseWriter, r *http.Request) {
	sessionID, err := portalPathID(r, "sessionId")
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.posaulaPraticasDaAula(r.Context(), sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePortalDeleteTask (DELETE /portal/tasks/{taskId}) → 204. Soft: a
// prática some pro aluno e fica no histórico como excluída. É o único
// "veto" do professor sobre o que o Claude gerou.
func (s *Server) handlePortalDeleteTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := portalPathID(r, "taskId")
	if err != nil {
		writeErr(w, err)
		return
	}
	sessionID, err := s.posaulaExcluirPratica(r.Context(), taskID)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "posaula_pratica_excluida", "posaula_task", fmt.Sprint(taskID), map[string]any{"sessionId": fmt.Sprint(sessionID)})
	w.WriteHeader(http.StatusNoContent)
}

// handlePortalRegenerateTasks (POST /portal/sessions/{sessionId}/tasks/regenerate)
// → 202 {queued:true}. Exige diário (404 senão). A "versão" que entra na
// chave de idempotência é o instante do pedido, não o updated_at do diário:
// com o updated_at, pedir de novo sem editar o diário seria engolido como
// "já gerei isso" — e o botão existe justamente pra esse caso. Com uma
// geração ATIVA o pedido vira uma segunda task agendada (mesmo caminho do
// diário salvo de novo, ver posaulaEnfileirarComConflito) — continua 202.
func (s *Server) handlePortalRegenerateTasks(w http.ResponseWriter, r *http.Request) {
	sessionID, err := portalPathID(r, "sessionId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.posaulaDiarioVersao(r.Context(), sessionID); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.enqueuePosaulaGerar(r.Context(), sessionID, time.Now()); err != nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", "Não consegui enfileirar a geração agora — tente de novo em instantes"))
		return
	}
	s.portalLogActivity(r, "posaula_regenerar", "session", fmt.Sprint(sessionID), nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}
