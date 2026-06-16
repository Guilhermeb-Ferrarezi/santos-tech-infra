package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) uid(r *http.Request) int64 { return r.Context().Value(userIDKey).(int64) }

// customer garante (idempotente, sem sobrescrever dados) o registro do cliente logado.
func (s *Server) customer(ctx context.Context, userID int64) (*Customer, error) {
	return s.store.UpsertCustomer(ctx, userID)
}

func (s *Server) handleGetMeCustomer(w http.ResponseWriter, r *http.Request) {
	c, err := s.customer(r.Context(), s.uid(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao carregar cliente")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handlePutMeCustomer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TaxID string `json:"taxId"`
		Phone string `json:"phone"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	taxID := onlyDigits(in.TaxID)
	phone := onlyDigits(in.Phone)
	if taxID != "" && !validCPF(taxID) {
		writeError(w, http.StatusBadRequest, "invalid_body", "CPF inválido (11 dígitos)")
		return
	}
	if !validPhone(phone) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Telefone inválido (10 ou 11 dígitos)")
		return
	}
	if _, err := s.customer(r.Context(), s.uid(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha")
		return
	}
	if err := s.store.UpdateCustomerData(r.Context(), s.uid(r), taxID, phone); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGetCart(w http.ResponseWriter, r *http.Request) {
	items, err := s.cart.List(r.Context(), s.uid(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "redis_error", "Falha no carrinho")
		return
	}
	// enriquece com dados do produto
	type cartLine struct {
		Product  Product `json:"product"`
		Quantity int     `json:"quantity"`
	}
	out := []cartLine{}
	for _, it := range items {
		p, err := s.store.GetProductByID(r.Context(), it.ProductID)
		if err == nil {
			out = append(out, cartLine{Product: *p, Quantity: it.Quantity})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddCart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Slug string `json:"slug"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	p, err := s.store.GetProductBySlug(r.Context(), in.Slug)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
		return
	}
	if err := s.cart.Add(r.Context(), s.uid(r), p.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "redis_error", "Falha ao adicionar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveCart(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.ParseInt(r.PathValue("productId"), 10, 64)
	if err != nil || pid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_param", "productId inválido")
		return
	}
	if err := s.cart.Remove(r.Context(), s.uid(r), pid); err != nil {
		writeError(w, http.StatusInternalServerError, "redis_error", "Falha ao remover")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TaxID string `json:"taxId"`
		Phone string `json:"phone"`
		Save  bool   `json:"save"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.TaxID = onlyDigits(in.TaxID)
	in.Phone = onlyDigits(in.Phone)
	if !validCPF(in.TaxID) {
		writeError(w, http.StatusBadRequest, "invalid_body", "CPF inválido (11 dígitos)")
		return
	}
	if !validPhone(in.Phone) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Telefone inválido (10 ou 11 dígitos)")
		return
	}
	uid := s.uid(r)
	cust, err := s.customer(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha no cliente")
		return
	}
	items, err := s.cart.List(r.Context(), uid)
	if err != nil || len(items) == 0 {
		writeError(w, http.StatusBadRequest, "empty_cart", "Carrinho vazio")
		return
	}
	// monta itens + total a partir do catálogo (preço sempre do servidor)
	var total int64
	chargeItems := []ChargeItem{}
	for _, it := range items {
		p, err := s.store.GetProductByID(r.Context(), it.ProductID)
		if err != nil {
			continue
		}
		pid := p.ID
		chargeItems = append(chargeItems, ChargeItem{ProductID: &pid, Name: p.Name, PriceCents: p.PriceCents, Quantity: it.Quantity})
		total += p.PriceCents * int64(it.Quantity)
	}
	if total <= 0 {
		writeError(w, http.StatusBadRequest, "empty_cart", "Carrinho inválido")
		return
	}
	cid := cust.ID
	c := &Charge{
		Kind: "avulso", CustomerID: &cid, AmountCents: total,
		DueDate:  time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
		Provider: "dotfy", CorrelationID: newCorrelationID(),
		PublicToken: newPublicToken(), payerTaxID: in.TaxID,
	}
	st := &Student{Name: "Cliente", TaxID: in.TaxID} // payerName/payerTaxId p/ o Dotfy
	if err := s.createAndPersistCharge(r.Context(), c, st, "Compra Santos Tech"); err != nil {
		// 422 (não 502): o Cloudflare/Traefik substitui respostas 5xx do origin por uma
		// página de erro HTML sem CORS, escondendo a mensagem. Com 4xx o cliente recebe o JSON.
		var pe *ProviderError
		if errors.As(err, &pe) {
			slog.Warn("checkout: erro do gateway", "status", pe.Status, "message", pe.Message)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("checkout: falha no provider", "err", err)
		writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao gerar a cobrança. Tente novamente.")
		return
	}
	if err := s.store.InsertChargeItems(r.Context(), c.ID, chargeItems); err != nil {
		// não falha o pagamento (cobrança já válida no Dotfy), mas registra para auditoria
		slog.Warn("falha ao gravar itens da cobrança", "charge_id", c.ID, "err", err)
	}
	_ = s.cart.Clear(r.Context(), uid)
	if in.Save {
		_ = s.store.UpdateCustomerData(r.Context(), uid, in.TaxID, in.Phone)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": c.PublicToken, "brCode": c.BRCode, "qrCode": c.QRCode, "amountCents": total,
	})
}

func (s *Server) handleMeCharges(w http.ResponseWriter, r *http.Request) {
	// Leitura: não cria o customer à toa. Sem customer ainda → histórico vazio.
	cust, err := s.store.GetCustomerByUserID(r.Context(), s.uid(r))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, []Charge{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha")
		return
	}
	list, err := s.store.ListChargesByCustomer(r.Context(), cust.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
