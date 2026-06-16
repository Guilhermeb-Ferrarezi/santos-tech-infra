package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Server) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	var in Product
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.Slug = strings.TrimSpace(in.Slug)
	in.Name = strings.TrimSpace(in.Name)
	if err := productValid(in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := s.store.CreateProduct(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar produto (slug duplicado?)")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleListProducts(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProducts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleUpdateProduct(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in Product
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.ID = id
	if err := productValid(in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := s.store.UpdateProduct(r.Context(), &in); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao atualizar")
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// público — usado pela tela de pagamento antes do login
func (s *Server) handleGetProductBySlug(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProductBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, p)
}
