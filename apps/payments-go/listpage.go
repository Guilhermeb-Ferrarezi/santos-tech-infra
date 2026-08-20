package main

import (
	"net/http"
	"strconv"
)

// Paginação das listagens.
//
// As listagens do serviço faziam SELECT ... ORDER BY created_at DESC sem LIMIT: em
// tabelas que só crescem (cobranças, movimentos, clientes), cada request carregava a
// tabela inteira para a memória do processo e para o JSON da resposta. Com um ano de
// operação isso deixa de ser "listagem" e vira despejo de banco.
//
// O contrato da resposta continua sendo um ARRAY (nada de envelope), para não quebrar
// o frontend: quem quiser a página seguinte passa ?before=<id da última linha>.
const (
	// defaultListLimit é o teto quando o cliente não pede nada.
	defaultListLimit = 200
	// maxListLimit é o teto absoluto, mesmo que o cliente peça mais.
	maxListLimit = 1000
)

// listPage descreve uma página: teto de linhas e cursor (id exclusivo, decrescente).
type listPage struct {
	Limit int32
	// BeforeID = 0 significa primeira página. Caso contrário, só linhas com id menor.
	BeforeID int64
}

// defaultPage devolve a primeira página com o teto padrão. Usada por chamadores
// internos (jobs, e-mail, relatórios) que não vêm de um request HTTP.
func defaultPage() listPage {
	return listPage{Limit: defaultListLimit}
}

// pageFromRequest lê ?limit= e ?before= do request, com teto e validação.
// Valores ausentes, inválidos ou fora da faixa caem no padrão — nunca em "sem limite".
func pageFromRequest(r *http.Request) listPage {
	p := defaultPage()
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > maxListLimit {
				n = maxListLimit
			}
			p.Limit = int32(n)
		}
	}
	if v := r.URL.Query().Get("before"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			p.BeforeID = n
		}
	}
	return p
}
