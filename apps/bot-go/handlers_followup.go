package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// As rotas do acompanhamento pós-aula.
//
// Tudo aqui responde a uma pergunta que a escola não conseguia fazer: dos leads
// que o bot qualificou e marcou, quantos apareceram e quantos viraram aluno.

// GET /api/followup?dias=60 — as aulas do bot com o desfecho de cada uma.
//
// Vem com o resumo junto, no mesmo envelope, de propósito: a conta é da MESMA
// lista que está na tela. Somar no servidor e listar no cliente é como os dois
// números começam a discordar.
func (s *Server) handleDashFollowup(w http.ResponseWriter, r *http.Request) {
	if s.followup == nil {
		jsonErr(w, "follow-up não configurado", http.StatusServiceUnavailable)
		return
	}
	dias := 60
	if v := r.URL.Query().Get("dias"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 730 {
			dias = n
		}
	}
	limite := 0
	if v := r.URL.Query().Get("limite"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limite = n
		}
	}

	desde := time.Now().AddDate(0, 0, -dias)
	linhas, err := s.followup.Lista(r.Context(), TenantID(s.cfg.TenantID), desde, limite)
	if err != nil {
		s.logger.Error("dash: listar follow-up", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	if linhas == nil {
		linhas = []LinhaFollowup{}
	}
	jsonOK(w, map[string]any{
		"aulas":  linhas,
		"resumo": Resume(linhas),
		"dias":   dias,
	})
}

// PATCH /api/followup/{pageId} — marca o que aconteceu com uma aula.
//
// É o clique que a coordenação dá: faltou · veio · veio e fechou · veio e não
// fechou. Remarcável à vontade — "veio" costuma virar "fechou" dias depois, e um
// registro que não dá para corrigir é um registro que ninguém preenche.
func (s *Server) handleDashMarcaResultado(w http.ResponseWriter, r *http.Request) {
	if s.followup == nil {
		jsonErr(w, "follow-up não configurado", http.StatusServiceUnavailable)
		return
	}
	pageID := r.PathValue("pageId")
	if pageID == "" {
		jsonErr(w, "aula não informada", http.StatusBadRequest)
		return
	}

	var body struct {
		Resultado  string `json:"resultado"`
		Observacao string `json:"observacao"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !ResultadoValido(body.Resultado) {
		jsonErr(w, "resultado inválido", http.StatusBadRequest)
		return
	}

	// Quem marcou fica no registro. Meses depois, "veio e fechou" sem autor é
	// uma afirmação que ninguém consegue confirmar nem corrigir.
	quem := quemEstaPedindo(r)

	err := s.followup.MarcaResultado(r.Context(), TenantID(s.cfg.TenantID),
		pageID, body.Resultado, limpaTextoDoCliente(body.Observacao, 300), quem)
	if errors.Is(err, ErrAulaDesconhecida) {
		jsonErr(w, "aula não encontrada", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("dash: marcar resultado da aula", "err", err, "aula", pageID)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("follow-up: resultado marcado",
		"aula", pageID, "resultado", body.Resultado, "por", quem)
	jsonOK(w, map[string]bool{"ok": true})
}

// PATCH /api/avaliacoes/{telefone} — em que pé está o convite para avaliar.
//
// ⚠️ `avaliou` é anotação da escola, não fato verificado: o Google não diz quem
// avaliou. Ver a migration 0041.
func (s *Server) handleDashMarcaAvaliacao(w http.ResponseWriter, r *http.Request) {
	if s.followup == nil {
		jsonErr(w, "follow-up não configurado", http.StatusServiceUnavailable)
		return
	}
	telefone := soDigitos(r.PathValue("telefone"))
	if telefone == "" {
		jsonErr(w, "telefone inválido", http.StatusBadRequest)
		return
	}

	var body struct {
		Status     string `json:"status"`
		Observacao string `json:"observacao"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !StatusAvaliacaoValido(body.Status) {
		jsonErr(w, "status inválido", http.StatusBadRequest)
		return
	}

	if err := s.followup.MarcaAvaliacao(r.Context(), TenantID(s.cfg.TenantID),
		telefone, body.Status, limpaTextoDoCliente(body.Observacao, 300)); err != nil {
		s.logger.Error("dash: marcar avaliação", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

// GET /api/avaliacoes/pendentes — quem teve aula e ainda não foi convidado.
//
// ⚠️ A lista NÃO separa quem fechou de quem não fechou. Convidar só quem comprou
// (ou só quem elogiou) é "review gating": proibido pelas políticas do Google e
// motivo de remoção do perfil. O que esta fila controla é não pedir duas vezes.
func (s *Server) handleDashAvaliacoesPendentes(w http.ResponseWriter, r *http.Request) {
	if s.followup == nil {
		jsonErr(w, "follow-up não configurado", http.StatusServiceUnavailable)
		return
	}
	linhas, err := s.followup.AguardandoConviteDeAvaliacao(r.Context(), TenantID(s.cfg.TenantID), 0)
	if err != nil {
		s.logger.Error("dash: listar avaliações pendentes", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	if linhas == nil {
		linhas = []LinhaFollowup{}
	}
	jsonOK(w, map[string]any{"pendentes": linhas})
}

// quemEstaPedindo identifica o autor de uma marcação para o registro.
//
// O painel autentica por sessão de admin, mas o bot-go não guarda o usuário —
// ele só pergunta ao auth central se a sessão é de administrador. Então aqui vai
// o que dá para afirmar com honestidade: "painel" quando veio uma sessão, ou
// "integração" quando veio pela chave de API. Inventar um nome de pessoa seria
// pior que não ter nenhum.
func quemEstaPedindo(r *http.Request) string {
	if r.Header.Get("X-Dash-Key") != "" || r.Header.Get("Authorization") != "" {
		return "integração"
	}
	return "painel"
}
