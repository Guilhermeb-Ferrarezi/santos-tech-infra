package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// EfiBalance agrupa o saldo disponível e os campos complementares.
// A API Pix da Efí só expõe o saldo total disponível; os demais campos
// (processingCents, blockedCents, dailyLimitCents, nightLimitCents) não possuem
// endpoint equivalente neste gateway — retornamos 0 por ora.
// TODO: consultar endpoint de limites/extrato da Efí quando disponível ou usar
// dados de analytics local para estimar os valores em processamento/bloqueados.
type EfiBalance struct {
	AvailableCents  int64 `json:"availableCents"`
	ProcessingCents int64 `json:"processingCents"` // TODO: sem endpoint Efí → sempre 0
	BlockedCents    int64 `json:"blockedCents"`    // TODO: sem endpoint Efí → sempre 0
	DailyLimitCents int64 `json:"dailyLimitCents"` // TODO: sem endpoint Efí → sempre 0
	NightLimitCents int64 `json:"nightLimitCents"` // TODO: sem endpoint Efí → sempre 0
}

// withdrawalStore isola as operações de persistência de saques.
type withdrawalStore interface {
	CreateWithdrawal(ctx context.Context, w *Withdrawal) error
	ListWithdrawals(ctx context.Context) ([]Withdrawal, error)
	// GetWithdrawalByIdempotencyKey devolve o withdrawal existente com aquela chave,
	// ou nil (sem erro) se não existir. Usado para dedupe de saques.
	GetWithdrawalByIdempotencyKey(ctx context.Context, key string) (*Withdrawal, error)
}

// efiOps isola as operações Efí expostas via dashboard (o *efiProvider em prod, fake nos testes).
type efiOps interface {
	GetBalance(ctx context.Context) (int64, error)
	// SendPix executa um Pix Envio (payout) via PUT /v3/gn/pix/{idEnvio}. idEnvio é a chave
	// de idempotência; favorecidoChave é a chave Pix de destino; infoPagador é a descrição.
	// Devolve idEnvio/e2eId/status da Efí (status EM_PROCESSAMENTO é normal — não-final).
	SendPix(ctx context.Context, idEnvio string, valorCents int64, favorecidoChave, infoPagador string) (idEnvioOut, e2eID, status string, err error)
	// CancelCharge remove uma cobrança ativa no gateway (invalida o QR na hora).
	CancelCharge(ctx context.Context, providerChargeID string) error
	// GetReceipt baixa o comprovante de um Pix pelo txid (= correlationID da cobrança).
	// Retorna o content-type e os bytes do comprovante.
	GetReceipt(ctx context.Context, txid string) (contentType string, body []byte, err error)
	// ListMED lista infrações MED (disputas Pix) na janela [inicio, fim].
	ListMED(ctx context.Context, inicio, fim time.Time) ([]MEDInfraction, error)
	// RequestReport solicita geração do extrato de conciliação Pix para a data dada
	// (formato YYYY-MM-DD). Retorna o ID do relatório (resposta 202 da Efí).
	RequestReport(ctx context.Context, dataMovimento string) (reportID string, err error)
	// GetReport consulta o status do relatório. Retorna ("done", contentType, csv, nil)
	// quando pronto (200) ou ("processing", "", nil, nil) quando ainda processando (202).
	GetReport(ctx context.Context, id string) (status string, contentType string, body []byte, err error)
	// CreateRecurrence cria um contrato de PIX Automático (jornada 2: só autoriza).
	CreateRecurrence(ctx context.Context, req RecurrenceRequest) (RecurrenceResult, error)
	// CreateRecurrenceJornada3 cria a recorrência + 1ª cobr (vencimento hoje) num único
	// QR de autorização+pagamento. Devolve o contrato e a 1ª cobr.
	CreateRecurrenceJornada3(ctx context.Context, req RecurrenceJornada3Request) (RecurrenceResult, ChargeResult, error)
	// GetRecurrence consulta o status do contrato de recorrência (status mapeado).
	GetRecurrence(ctx context.Context, idRec string) (status string, err error)
	// CancelRecurrence cancela o contrato de recorrência na Efí (best-effort).
	CancelRecurrence(ctx context.Context, idRec string) error
	// CreateRecurringCharge cria a cobrança de um ciclo, vinculada ao contrato via idRec.
	CreateRecurringCharge(ctx context.Context, req RecurringChargeRequest) (ChargeResult, error)
	// ParseRecWebhook mapeia o payload do webhook de recorrências em eventos de status.
	ParseRecWebhook(headers map[string][]string, body []byte) ([]RecEvent, error)
	// RegisterRecWebhook registra a URL do webhook de recorrências na Efí.
	RegisterRecWebhook(ctx context.Context, url string) error
}

// chargeReader isola o acesso a cobranças no banco (o *Store em prod, fake nos testes).
type chargeReader interface {
	GetCharge(ctx context.Context, id int64) (*Charge, error)
}

func (s *Server) handleEfiMED(w http.ResponseWriter, r *http.Request) {
	rng := parseRange(r.URL.Query().Get("range"))
	items, err := s.efi.ListMED(r.Context(), rng.From, rng.To)
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar infrações MED na Efí")
		return
	}
	if items == nil {
		items = []MEDInfraction{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleEfiBalance(w http.ResponseWriter, r *http.Request) {
	cents, err := s.efi.GetBalance(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar saldo na Efí")
		return
	}
	// processingCents, blockedCents, dailyLimitCents e nightLimitCents não são expostos
	// pela API Pix Efí via endpoint único. Retornamos 0 e anotamos o TODO.
	writeJSON(w, http.StatusOK, EfiBalance{
		AvailableCents:  cents,
		ProcessingCents: 0, // TODO: sem endpoint Efí disponível
		BlockedCents:    0, // TODO: sem endpoint Efí disponível
		DailyLimitCents: 0, // TODO: sem endpoint Efí disponível
		NightLimitCents: 0, // TODO: sem endpoint Efí disponível
	})
}

// handleListWithdrawals (GET /withdrawals) lista todos os saques. Requer admin.
func (s *Server) handleListWithdrawals(w http.ResponseWriter, r *http.Request) {
	ws := s.withdrawalStoreOf()
	if ws == nil {
		writeError(w, http.StatusServiceUnavailable, "db_unavailable", "Banco de dados não disponível")
		return
	}
	list, err := ws.ListWithdrawals(r.Context())
	if err != nil {
		slog.Warn("withdrawals: falha ao listar", "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar saques")
		return
	}
	if list == nil {
		list = []Withdrawal{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleReceipt(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := s.charges.GetCharge(r.Context(), id)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	if c.Status != "paid" {
		writeError(w, http.StatusConflict, "not_paid", "Cobrança ainda não foi paga")
		return
	}
	ct, body, err := s.efi.GetReceipt(r.Context(), c.CorrelationID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao obter comprovante na Efí")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="comprovante-`+r.PathValue("id")+`.pdf"`)
	w.Write(body) //nolint:errcheck
}

// handleReportRequest solicita a geração de um relatório de extrato de conciliação.
// POST /efi/reports   body: {"date":"YYYY-MM-DD"}   → {"reportId":"..."}
func (s *Server) handleReportRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Date string `json:"date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Date == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Campo date obrigatório (YYYY-MM-DD)")
		return
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Campo date deve ser YYYY-MM-DD")
		return
	}
	id, err := s.efi.RequestReport(r.Context(), req.Date)
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao solicitar relatório na Efí")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reportId": id})
}

// handleReportGet consulta o status/conteúdo de um relatório de conciliação.
// GET /efi/reports/{id}
//   - processando → 200 {"status":"processing"}
//   - pronto      → 200 text/csv com Content-Disposition
func (s *Server) handleReportGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "ID do relatório obrigatório")
		return
	}
	status, ct, body, err := s.efi.GetReport(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar relatório na Efí")
		return
	}
	if status == "processing" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "processing"})
		return
	}
	// status == "done": streama o CSV
	if ct == "" {
		ct = "text/csv"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="extrato-conciliacao-`+id+`.csv"`)
	w.Write(body) //nolint:errcheck
}
