package main

import (
	"net/http"
	"strings"
)

// Handlers do gerador de currículo, fase 2 — só o botão "Melhorar com IA".
// Self-service: qualquer sessão autenticada (authGuard), sem exigir
// permissão de staff — o mesmo espírito de /portal/me/*, porque um aluno usa
// isto pra reescrever o PRÓPRIO currículo.

type curriculoReescreverBody struct {
	Campo    string            `json:"campo"`
	Texto    string            `json:"texto"`
	MaxChars int               `json:"maxChars"`
	Contexto curriculoContexto `json:"contexto"`
}

func (in curriculoReescreverBody) validate() error {
	if !curriculoCampoValido(in.Campo) {
		return validationErr("campo inválido")
	}
	if strings.TrimSpace(in.Texto) == "" {
		return validationErr("texto é obrigatório")
	}
	if len([]rune(in.Texto)) > curriculoTextoOriginalMax {
		return validationErr("texto muito longo")
	}
	return nil
}

// handleCurriculoReescrever (POST /portal/curriculo/reescrever) body
// {campo, texto, maxChars, contexto} → 202 {id, queued:true}. A tela consulta
// o resultado em GET /portal/curriculo/reescrever/{id}.
func (s *Server) handleCurriculoPostReescrever(w http.ResponseWriter, r *http.Request) {
	var in curriculoReescreverBody
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	userID := userIDFrom(r)
	id, err := s.enqueueCurriculoReescrever(r.Context(), userID, curriculoEnqueueInput{
		Campo: in.Campo, Texto: in.Texto, MaxChars: in.MaxChars, Contexto: in.Contexto,
	})
	if err != nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", "Não consegui pedir a reescrita agora — tente de novo em instantes"))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "queued": true})
}

// handleCurriculoGetReescrever (GET /portal/curriculo/reescrever/{id}) →
// {id, status, error?, textoReescrito?}. 404 se a linha não é do usuário (ou
// não existe) — a tela para de consultar quando vê 404.
func (s *Server) handleCurriculoGetReescrever(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := userIDFrom(r)
	out, err := s.curriculoBuscarStatus(r.Context(), userID, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if out == nil {
		writeErr(w, notFoundErr("Pedido de reescrita"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
