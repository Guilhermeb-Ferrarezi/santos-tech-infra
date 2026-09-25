package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Rotas da tela "Como o bot vende" — regras de venda e vocabulário
// (spec 2026-09-25-bot-regras-venda-editaveis, no repo dashboard).
//
// Fora do PATCH /api/config de propósito: a tela Configurações salva o
// objeto de config inteiro e sobrescreveria as regras.

// maxCorpoRegras — 9 partes de até 4.000 caracteres + vocabulário cabem com
// folga; acima disso é erro ou abuso.
const maxCorpoRegras = 128 << 10

func (s *Server) rotasDeVendas(mux *http.ServeMux) {
	da := s.dashMiddleware
	mux.Handle("GET /api/vendas/regras", da(s.handleVendasRegras))
	mux.Handle("PATCH /api/vendas/regras", da(s.handleVendasSalvaRegras))
	mux.Handle("GET /api/vendas/regras/versoes", da(s.handleVendasVersoes))
	mux.Handle("GET /api/vendas/regras/versoes/{id}", da(s.handleVendasVersao))
	mux.Handle("POST /api/vendas/regras/versoes/{id}/restaurar", da(s.handleVendasRestaura))
	mux.Handle("GET /api/vendas/vocabulario/ocorrencias", da(s.handleVendasOcorrencias))
	// Playbook: fichas de situação (handlers_playbook.go)
	mux.Handle("GET /api/vendas/situacoes", da(s.handleSituacoes))
	mux.Handle("POST /api/vendas/situacoes", da(s.handleCriaSituacao))
	mux.Handle("PATCH /api/vendas/situacoes/{id}", da(s.handleEditaSituacao))
}

// versaoNaTela — o que a tela precisa saber da versão em vigor.
type versaoNaTela struct {
	ID           string    `json:"id"`
	SalvoPor     string    `json:"salvoPor"`
	SalvoEm      time.Time `json:"salvoEm"`
	RestauradaDe string    `json:"restauradaDe,omitempty"`
}

// respostaRegrasVenda — GET e resposta do PATCH/restaurar.
//
//	regras      — como está salvo (vazio = padrão naquela parte)
//	padrao      — o padrão do código, completo, para a tela mostrar e comparar
//	padraoMudou — partes personalizadas cujo padrão mudou depois da edição
type respostaRegrasVenda struct {
	Versao      *versaoNaTela `json:"versao"`
	Regras      RegrasVenda   `json:"regras"`
	Padrao      RegrasVenda   `json:"padrao"`
	Travas      []string      `json:"travas"`
	Partes      []ParteRegra  `json:"partes"`
	PadraoMudou []ParteRegra  `json:"padraoMudou"`
}

func montaRespostaRegras(v VersaoRegras) respostaRegrasVenda {
	out := respostaRegrasVenda{
		Regras:      v.Regras,
		Padrao:      PadraoRegrasVenda(),
		Travas:      TravasDeHonestidade,
		Partes:      PartesDasRegras,
		PadraoMudou: []ParteRegra{},
	}
	if out.Regras.Textos == nil {
		out.Regras.Textos = map[ParteRegra]string{}
	}
	if v.ID != "" {
		out.Versao = &versaoNaTela{ID: v.ID, SalvoPor: v.SalvoPor, SalvoEm: v.SalvoEm, RestauradaDe: v.RestauradaDe}
		atual := hashesDoPadrao()
		for _, parte := range PartesDasRegras {
			if v.Regras.Textos[parte] != "" && v.PadraoHash[parte] != "" && v.PadraoHash[parte] != atual[parte] {
				out.PadraoMudou = append(out.PadraoMudou, parte)
			}
		}
	}
	return out
}

func (s *Server) tenantDoPainel() TenantID { return TenantID(s.cfg.TenantID) }

// GET /api/vendas/regras
func (s *Server) handleVendasRegras(w http.ResponseWriter, r *http.Request) {
	v, err := s.regrasVenda.Atual(r.Context(), s.tenantDoPainel())
	if err != nil {
		s.logger.Error("vendas: ler regras", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, montaRespostaRegras(v))
}

// PATCH /api/vendas/regras — { versaoBase, regras }. Grava versão nova.
func (s *Server) handleVendasSalvaRegras(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCorpoRegras)
	var body struct {
		VersaoBase string      `json:"versaoBase"`
		Regras     RegrasVenda `json:"regras"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var grande *http.MaxBytesError
		if errors.As(err, &grande) {
			jsonErr(w, "texto grande demais", http.StatusRequestEntityTooLarge)
			return
		}
		jsonErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	v, err := s.regrasVenda.Salvar(r.Context(), s.tenantDoPainel(), body.Regras, body.VersaoBase, quemEstaPedindo(r))
	s.respondeVersaoGravada(w, v, err, "salvar")
}

// GET /api/vendas/regras/versoes — as últimas 20, da mais nova para a mais velha.
func (s *Server) handleVendasVersoes(w http.ResponseWriter, r *http.Request) {
	lista, err := s.regrasVenda.Versoes(r.Context(), s.tenantDoPainel(), 20)
	if err != nil {
		s.logger.Error("vendas: listar versões", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"versoes": lista})
}

// GET /api/vendas/regras/versoes/{id}
func (s *Server) handleVendasVersao(w http.ResponseWriter, r *http.Request) {
	v, err := s.regrasVenda.Versao(r.Context(), s.tenantDoPainel(), strings.TrimSpace(r.PathValue("id")))
	if errors.Is(err, ErrVersaoNaoEncontrada) {
		jsonErr(w, "versão não encontrada", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("vendas: ler versão", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, v)
}

// POST /api/vendas/regras/versoes/{id}/restaurar — copia a versão como nova.
func (s *Server) handleVendasRestaura(w http.ResponseWriter, r *http.Request) {
	v, err := s.regrasVenda.Restaurar(r.Context(), s.tenantDoPainel(), strings.TrimSpace(r.PathValue("id")), quemEstaPedindo(r))
	s.respondeVersaoGravada(w, v, err, "restaurar")
}

func (s *Server) respondeVersaoGravada(w http.ResponseWriter, v VersaoRegras, err error, acao string) {
	switch {
	case errors.Is(err, ErrRegrasInvalidas):
		jsonErr(w, strings.TrimPrefix(err.Error(), ErrRegrasInvalidas.Error()+": "), http.StatusBadRequest)
		return
	case errors.Is(err, ErrVersaoDesatualizada):
		// Código, não frase: a tela reconhece e mostra o aviso em português.
		jsonErr(w, "stale_version", http.StatusConflict)
		return
	case errors.Is(err, ErrVersaoNaoEncontrada):
		jsonErr(w, "versão não encontrada", http.StatusNotFound)
		return
	case err != nil:
		s.logger.Error("vendas: "+acao+" regras", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A próxima mensagem do bot já usa a versão nova.
	s.regrasFonte.Invalida(s.tenantDoPainel())
	s.logger.Info("vendas: regras gravadas", "acao", acao, "versao", v.ID, "alteracoes", v.Alteracoes)
	jsonOK(w, montaRespostaRegras(v))
}
