package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// productStoreOf devolve s.products se configurado (injeção de teste), senão s.store.
func (s *Server) productStoreOf() productStore {
	if s.products != nil {
		return s.products
	}
	if s.store != nil {
		return s.store
	}
	return nil
}

func (s *Server) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	var in Product
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.Slug = strings.TrimSpace(in.Slug)
	in.Name = strings.TrimSpace(in.Name)
	in.ImageURL = strings.TrimSpace(in.ImageURL)
	in.FileURL = strings.TrimSpace(in.FileURL)
	if err := productValid(in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := s.productStoreOf().CreateProduct(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar produto (slug duplicado?)")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleListProducts(w http.ResponseWriter, r *http.Request) {
	list, err := s.productStoreOf().ListProducts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGetProduct retorna um produto pelo id (admin).
func (s *Server) handleGetProduct(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	p, err := s.productStoreOf().GetProductByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUpdateProduct(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in Product
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.ID = id
	in.ImageURL = strings.TrimSpace(in.ImageURL)
	in.FileURL = strings.TrimSpace(in.FileURL)
	if err := productUpdateValid(in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := s.productStoreOf().UpdateProduct(r.Context(), &in); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao atualizar")
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleDeleteProduct(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.productStoreOf().DeleteProduct(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao excluir")
		return
	}
	// 200 com corpo (não 204): clientes que parseiam JSON da resposta quebram com um
	// 204 sem corpo (ex: o authApi do account-kit faz res.json()).
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGetProductBySlug (GET /products/by-slug/{slug}) — PÚBLICO, usado pela tela de
// pagamento antes do login. Responde o DTO reduzido: serializar o Product inteiro
// entregava o fileUrl (o produto comprado) a qualquer um que soubesse o slug.
func (s *Server) handleGetProductBySlug(w http.ResponseWriter, r *http.Request) {
	p, err := s.productStoreOf().GetProductBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, publicProduct(*p))
}

// handleGetMeProductFile (GET /me/products/{id}/file) entrega o arquivo do produto
// SOMENTE a quem tem cobrança paga dele. É a única rota que revela o file_url.
func (s *Server) handleGetMeProductFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_param", "id inválido")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusNotFound, "not_found", "Arquivo não disponível")
		return
	}
	owns, err := s.store.UserOwnsProduct(r.Context(), s.uid(r), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao verificar a compra")
		return
	}
	if !owns {
		// 404 (não 403) de propósito: não confirma sequer que o produto existe.
		writeError(w, http.StatusNotFound, "not_found", "Arquivo não disponível")
		return
	}
	p, err := s.store.GetProductByID(r.Context(), id)
	if err != nil || strings.TrimSpace(p.FileURL) == "" {
		writeError(w, http.StatusNotFound, "not_found", "Arquivo não disponível")
		return
	}
	http.Redirect(w, r, p.FileURL, http.StatusFound)
}
