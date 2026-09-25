package main

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// Perguntas ao usuário (Claude Design).
//
// O CLI só oferece a ferramenta AskUserQuestion quando há um host para responder.
// Com --permission-prompt-tool stdio ele pausa o turno ao chamá-la e emite no stdout
//
//	{"type":"control_request","request_id":"…","request":{"subtype":"can_use_tool",
//	 "tool_name":"AskUserQuestion","input":{"questions":[…]},"tool_use_id":"…"}}
//
// e espera no stdin um control_response com as respostas em updatedInput.answers.
// O modelo recebe o tool_result e continua o MESMO turno. Formato verificado contra o
// CLI 2.1.282 (spike de 2026-09-25, ver a spec claude-design-v2 no repo dashboard).
//
// As demais ferramentas seguem liberadas pelo --dangerously-skip-permissions e nunca
// chegam aqui; qualquer outra ferramenta interativa é recusada na hora, para o turno
// nunca ficar pendurado esperando uma interface que o painel não tem.

const askUserQuestionTool = "AskUserQuestion"

// questionTimeout é quanto uma pergunta espera por resposta antes de ser recusada
// sozinha. Enquanto ela espera, o watchdog de turno fica desarmado (o tempo do humano
// não conta), então este é o teto real de um turno parado numa pergunta.
const questionTimeout = 30 * time.Minute

const (
	msgPerguntaPulada         = "O usuário preferiu não responder às perguntas. Decida você com bom senso, diga em uma frase o que decidiu e siga com o trabalho."
	msgPerguntaSemTempo       = "O usuário não respondeu a tempo. Decida você com bom senso, diga em uma frase o que decidiu e siga com o trabalho."
	msgPerguntaParada         = "O usuário interrompeu o turno."
	msgFerramentaNaoSuportada = "Ferramenta interativa não suportada neste painel. Siga sem ela."
)

// maxAnswerRunes limita cada resposta (o "Outro" é texto livre).
const maxAnswerRunes = 2000

// pendingQuestion é a pergunta que pausou o turno atual.
type pendingQuestion struct {
	RequestID string
	ToolUseID string
	Input     map[string]any
	timer     *time.Timer
}

// event monta o evento de WebSocket que leva o formulário ao painel.
func (q *pendingQuestion) event() turnEvent {
	return turnEvent{Type: "question", Data: map[string]any{
		"requestId": q.RequestID,
		"toolUseId": q.ToolUseID,
		"questions": q.Input["questions"],
	}}
}

// questionTexts devolve o texto de cada pergunta do input da ferramenta — é a chave
// que o CLI espera em answers.
func questionTexts(input map[string]any) map[string]bool {
	out := map[string]bool{}
	qs, _ := input["questions"].([]any)
	for _, raw := range qs {
		q, _ := raw.(map[string]any)
		if t, _ := q["question"].(string); t != "" {
			out[t] = true
		}
	}
	return out
}

// sanitizeAnswers mantém só respostas a perguntas que existem, sem vazias, com teto
// de tamanho. O painel é admin, mas o texto vira tool_result no contexto do modelo.
func sanitizeAnswers(input map[string]any, answers map[string]string) map[string]string {
	valid := questionTexts(input)
	out := map[string]string{}
	for q, a := range answers {
		a = strings.TrimSpace(a)
		if !valid[q] || a == "" {
			continue
		}
		if r := []rune(a); len(r) > maxAnswerRunes {
			a = string(r[:maxAnswerRunes])
		}
		out[q] = a
	}
	return out
}

// controlResponseLine serializa a resposta a um control_request do CLI.
func controlResponseLine(requestID string, response map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	})
	return append(b, '\n')
}

// controlErrorLine responde a um control_request cujo subtipo o agent-go não trata.
func controlErrorLine(requestID, msg string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      msg,
		},
	})
	return append(b, '\n')
}

func allowWithAnswers(input map[string]any, answers map[string]string) map[string]any {
	updated := make(map[string]any, len(input)+1)
	for k, v := range input {
		updated[k] = v
	}
	updated["answers"] = answers
	return map[string]any{"behavior": "allow", "updatedInput": updated}
}

func denyWith(msg string) map[string]any {
	return map[string]any{"behavior": "deny", "message": msg}
}

// writeLine escreve uma linha no stdin do CLI. Serializado: prompt, interrupt e
// respostas de controle vêm de goroutines diferentes e não podem se intercalar.
func (ls *liveSession) writeLine(line []byte) error {
	ls.wmu.Lock()
	defer ls.wmu.Unlock()
	if ls.stdin == nil {
		return nil
	}
	_, err := ls.stdin.Write(line)
	return err
}

// handleControlRequest trata um pedido do CLI ao host (hoje: permissão de ferramenta).
func (ls *liveSession) handleControlRequest(ev map[string]any) {
	reqID, _ := ev["request_id"].(string)
	req, _ := ev["request"].(map[string]any)
	if req["subtype"] != "can_use_tool" {
		slog.Warn("control_request não suportado", "conv", ls.conv.ID, "subtype", req["subtype"])
		_ = ls.writeLine(controlErrorLine(reqID, "não suportado"))
		return
	}
	tool, _ := req["tool_name"].(string)
	input, _ := req["input"].(map[string]any)
	if tool != askUserQuestionTool || input == nil {
		_ = ls.writeLine(controlResponseLine(reqID, denyWith(msgFerramentaNaoSuportada)))
		return
	}
	toolUseID, _ := req["tool_use_id"].(string)

	ls.mu.Lock()
	if ls.question != nil {
		// Uma pergunta por vez: o CLI não deveria mandar outra antes de a primeira
		// ser respondida, mas se mandar, a segunda é recusada em vez de sobrescrever.
		ls.mu.Unlock()
		_ = ls.writeLine(controlResponseLine(reqID, denyWith(msgFerramentaNaoSuportada)))
		return
	}
	q := &pendingQuestion{RequestID: reqID, ToolUseID: toolUseID, Input: input}
	q.timer = time.AfterFunc(questionTimeout, func() {
		ls.resolveQuestion(reqID, nil, msgPerguntaSemTempo)
	})
	ls.question = q
	ls.disarmWatchdogLocked() // o teto de turno não conta o tempo de resposta do humano
	ev2 := q.event()
	ls.mu.Unlock()

	ls.mgr.dispatch(ls.conv.ID, ev2)
}

// handleControlCancel trata o CLI desistindo de um control_request (ex.: turno
// interrompido por dentro). A pergunta some do painel.
func (ls *liveSession) handleControlCancel(ev map[string]any) {
	reqID, _ := ev["request_id"].(string)
	ls.mu.Lock()
	q := ls.question
	if q == nil || q.RequestID != reqID {
		ls.mu.Unlock()
		return
	}
	ls.question = nil
	q.timer.Stop()
	if ls.state == StatusRunning {
		ls.armWatchdogLocked()
	}
	ls.mu.Unlock()
	ls.mgr.dispatch(ls.conv.ID, turnEvent{Type: "question_closed", Data: map[string]any{"requestId": reqID}})
}

// resolveQuestion responde a pergunta pendente: com respostas (allow) ou recusando com
// denyMsg. Devolve false se requestID não é a pergunta pendente (já respondida,
// expirada ou de outro processo).
func (ls *liveSession) resolveQuestion(requestID string, answers map[string]string, denyMsg string) bool {
	ls.mu.Lock()
	q := ls.question
	if q == nil || q.RequestID != requestID {
		ls.mu.Unlock()
		return false
	}
	ls.question = nil
	q.timer.Stop()
	if ls.state == StatusRunning {
		ls.armWatchdogLocked() // o turno volta a trabalhar: teto de duração de novo
	}
	ls.lastUsed = time.Now()
	ls.mu.Unlock()

	resp := denyWith(msgPerguntaPulada)
	switch {
	case denyMsg != "":
		resp = denyWith(denyMsg)
	case len(answers) > 0:
		resp = allowWithAnswers(q.Input, answers)
	}
	if err := ls.writeLine(controlResponseLine(requestID, resp)); err != nil {
		slog.Error("falha ao responder pergunta ao CLI", "conv", ls.conv.ID, "err", err)
	}
	ls.mgr.dispatch(ls.conv.ID, turnEvent{Type: "question_closed", Data: map[string]any{"requestId": requestID}})
	return true
}

// Answer aplica a resposta vinda do painel. skip=true (ou nenhuma resposta válida)
// recusa a pergunta pedindo ao modelo que decida sozinho.
func (ls *liveSession) Answer(requestID string, answers map[string]string, skip bool) bool {
	ls.mu.Lock()
	q := ls.question
	ls.mu.Unlock()
	if q == nil || q.RequestID != requestID {
		return false
	}
	if skip {
		return ls.resolveQuestion(requestID, nil, msgPerguntaPulada)
	}
	return ls.resolveQuestion(requestID, sanitizeAnswers(q.Input, answers), "")
}

// pendingQuestionEvent devolve o evento da pergunta pendente, para reenviar a um
// WebSocket que acabou de conectar (recarregar a página não pode perder o formulário).
func (ls *liveSession) pendingQuestionEvent() *turnEvent {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.question == nil {
		return nil
	}
	ev := ls.question.event()
	return &ev
}

// dropQuestion descarta a pendência sem responder (processo morreu).
func (ls *liveSession) dropQuestion() {
	ls.mu.Lock()
	q := ls.question
	ls.question = nil
	ls.mu.Unlock()
	if q != nil {
		q.timer.Stop()
		ls.mgr.dispatch(ls.conv.ID, turnEvent{Type: "question_closed", Data: map[string]any{"requestId": q.RequestID}})
	}
}

// AnswerQuestion roteia a resposta do painel para a sessão viva da conversa.
func (m *SessionManager) AnswerQuestion(convID, requestID string, answers map[string]string, skip bool) bool {
	m.mu.Lock()
	ls := m.live[convID]
	m.mu.Unlock()
	if ls == nil {
		return false
	}
	return ls.Answer(requestID, answers, skip)
}

// PendingQuestion devolve a pergunta pendente da conversa, se houver.
func (m *SessionManager) PendingQuestion(convID string) *turnEvent {
	m.mu.Lock()
	ls := m.live[convID]
	m.mu.Unlock()
	if ls == nil {
		return nil
	}
	return ls.pendingQuestionEvent()
}
