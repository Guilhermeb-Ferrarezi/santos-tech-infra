package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

// Rotas das fichas do playbook (aba "Playbook" da tela "Como o bot vende").
// Sem DELETE de propósito: arquivar não apaga, e a medição de uso continua
// apontando para a ficha.

const maxCorpoSituacao = 32 << 10

// motivoNaTela — os valores de motivacaoTipo, com o rótulo que a tela mostra.
type motivoNaTela struct {
	Valor  string `json:"valor"`
	Rotulo string `json:"rotulo"`
}

func motivosParaTela() []motivoNaTela {
	out := []motivoNaTela{}
	for v, r := range motivacoesValidas {
		out = append(out, motivoNaTela{v, r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rotulo < out[j].Rotulo })
	return out
}

// GET /api/vendas/situacoes?estado=
func (s *Server) handleSituacoes(w http.ResponseWriter, r *http.Request) {
	estado := strings.TrimSpace(r.URL.Query().Get("estado"))
	if estado != "" && !estadosDaFicha[estado] {
		jsonErr(w, "estado desconhecido", http.StatusBadRequest)
		return
	}
	lista, err := s.playbook.Lista(r.Context(), s.tenantDoPainel(), estado)
	if err != nil {
		s.logger.Error("playbook: listar fichas", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{
		"situacoes":           lista,
		"motivos":             motivosParaTela(),
		"maxNoPrompt":         maxSituacoesNoPrompt,
		"maxCaracteresPrompt": maxCaracteresSituacoes,
	})
}

func lerSituacao(w http.ResponseWriter, r *http.Request) (Situacao, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCorpoSituacao)
	var sit Situacao
	if err := json.NewDecoder(r.Body).Decode(&sit); err != nil {
		var grande *http.MaxBytesError
		if errors.As(err, &grande) {
			jsonErr(w, "texto grande demais", http.StatusRequestEntityTooLarge)
			return Situacao{}, false
		}
		jsonErr(w, "invalid body", http.StatusBadRequest)
		return Situacao{}, false
	}
	return sit, true
}

// POST /api/vendas/situacoes — ficha escrita no painel (origem sempre manual).
func (s *Server) handleCriaSituacao(w http.ResponseWriter, r *http.Request) {
	sit, ok := lerSituacao(w, r)
	if !ok {
		return
	}
	// O painel escreve "manual"; o Claude, a pedido do Henrique, anota
	// "claude". "ia" e "whatsapp" só o servidor atribui.
	if sit.Origem != "claude" {
		sit.Origem = "manual"
	}
	sit.ConversaID = ""
	nova, err := s.playbook.Cria(r.Context(), s.tenantDoPainel(), sit, quemEstaPedindo(r))
	s.respondeSituacao(w, nova, err, "criar")
}

// PATCH /api/vendas/situacoes/{id} — edita conteúdo e estado.
func (s *Server) handleEditaSituacao(w http.ResponseWriter, r *http.Request) {
	sit, ok := lerSituacao(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	editada, err := s.playbook.Edita(r.Context(), s.tenantDoPainel(), id, sit, quemEstaPedindo(r))
	s.respondeSituacao(w, editada, err, "editar")
}

func (s *Server) respondeSituacao(w http.ResponseWriter, sit Situacao, err error, acao string) {
	switch {
	case errors.Is(err, ErrSituacaoInvalida):
		jsonErr(w, strings.TrimPrefix(err.Error(), ErrSituacaoInvalida.Error()+": "), http.StatusBadRequest)
		return
	case errors.Is(err, ErrLimiteDeFichas):
		jsonErr(w, "o playbook chegou ao limite de fichas — arquive as que não servem mais", http.StatusBadRequest)
		return
	case errors.Is(err, ErrSituacaoNaoEncontrada):
		jsonErr(w, "ficha não encontrada", http.StatusNotFound)
		return
	case err != nil:
		s.logger.Error("playbook: "+acao+" ficha", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A próxima mensagem do bot já vê a mudança.
	s.playbookFonte.Invalida(s.tenantDoPainel())
	s.logger.Info("playbook: ficha gravada", "acao", acao, "id", sit.ID, "estado", sit.Estado)
	jsonOK(w, sit)
}
