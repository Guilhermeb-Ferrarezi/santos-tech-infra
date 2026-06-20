package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) uid(r *http.Request) int64 { return r.Context().Value(userIDKey).(int64) }

// handleListCustomers (admin) lista os clientes com agregados das compras.
func (s *Server) handleListCustomers(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListCustomersWithStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar clientes")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGetCustomer (admin) devolve um cliente + histórico de compras.
func (s *Server) handleGetCustomer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_param", "id inválido")
		return
	}
	d, err := s.store.GetCustomerDetail(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Cliente não encontrado")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao carregar cliente")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleGetMeCustomer(w http.ResponseWriter, r *http.Request) {
	uid := s.uid(r)
	c, err := s.store.GetCustomerByUserID(r.Context(), uid)
	if errors.Is(err, pgx.ErrNoRows) {
		// Sem cliente ainda (nenhuma compra). Devolve um vazio NÃO persistido para o
		// frontend pré-preencher — o cliente nasce no checkout, já com CPF.
		writeJSON(w, http.StatusOK, &Customer{UserID: uid})
		return
	}
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
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	taxID := onlyDigits(in.TaxID)
	phone := onlyDigits(in.Phone)
	// O cliente é identificado pelo CPF — sem CPF não há cliente a salvar.
	if !validCPF(taxID) {
		writeError(w, http.StatusBadRequest, "invalid_body", "CPF inválido (11 dígitos)")
		return
	}
	if !validPhone(phone) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Telefone inválido (10 ou 11 dígitos)")
		return
	}
	if _, err := s.store.UpsertCustomer(r.Context(), s.uid(r), taxID, phone, strings.TrimSpace(in.Name), strings.TrimSpace(in.Email)); err != nil {
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
	// Produto recorrente não entra no carrinho multi-item: a assinatura é item único e
	// passa pelo fluxo POST /me/subscribe (cria o contrato BACEN via Jornada 3). Sem este
	// guard, um cliente chamando /me/cart direto cobraria a assinatura 1x como avulso, sem
	// recorrência. O frontend já redireciona; aqui é a defesa no servidor.
	if p.Recurring {
		writeError(w, http.StatusConflict, "use_subscribe", "Produto recorrente: use a assinatura (/me/subscribe), não o carrinho")
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
		Name  string `json:"name"`
		Email string `json:"email"`
		// Save controla apenas o pré-preenchimento no frontend (localStorage). O
		// cliente é sempre registrado pelo CPF nesta cobrança, independentemente disso.
		Save bool `json:"save"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.TaxID = onlyDigits(in.TaxID)
	in.Phone = onlyDigits(in.Phone)
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.TrimSpace(in.Email)
	if !validName(in.Name) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Nome obrigatório (máximo 100 caracteres)")
		return
	}
	if !validEmail(in.Email) {
		writeError(w, http.StatusBadRequest, "invalid_body", "E-mail inválido")
		return
	}
	if !validCPF(in.TaxID) {
		writeError(w, http.StatusBadRequest, "invalid_body", "CPF inválido (11 dígitos)")
		return
	}
	if !validPhone(in.Phone) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Telefone inválido (10 ou 11 dígitos)")
		return
	}
	uid := s.uid(r)
	// Cliente único por (conta, CPF): cria/atualiza com os dados do pagador e liga a
	// cobrança a ele. É aqui que o cliente é materializado (sempre com CPF válido).
	cust, err := s.store.UpsertCustomer(r.Context(), uid, in.TaxID, in.Phone, in.Name, in.Email)
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
		// Se o produto virou recorrente DEPOIS de entrar no carrinho (admin marcou como
		// assinatura entre o add e o checkout), ele não pode ser cobrado como avulso. Em vez
		// de travar o checkout inteiro (cliente ficava preso), removemos o item do carrinho e
		// seguimos com o resto. Assinatura é por /me/subscribe.
		if p.Recurring {
			_ = s.cart.Remove(r.Context(), uid, it.ProductID)
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
		Provider: "efi", CorrelationID: newCorrelationID(),
		PublicToken: newPublicToken(), payerTaxID: in.TaxID,
	}
	st := &Student{Name: in.Name, TaxID: in.TaxID, Email: in.Email} // payerName/payerTaxId p/ o gateway
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
		// não falha o pagamento (cobrança já válida na Efí), mas registra para auditoria
		slog.Warn("falha ao gravar itens da cobrança", "charge_id", c.ID, "err", err)
	}
	_ = s.cart.Clear(r.Context(), uid)
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

// handleMeRecurrences lista as assinaturas (PIX Automático) do usuário logado,
// consolidadas pelo CPF do cliente. Sem customer ainda → lista vazia (200).
func (s *Server) handleMeRecurrences(w http.ResponseWriter, r *http.Request) {
	cust, err := s.store.GetCustomerByUserID(r.Context(), s.uid(r))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, []Recurrence{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha")
		return
	}
	list, err := s.store.ListRecurrencesByTaxID(r.Context(), cust.TaxID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
