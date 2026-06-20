package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// recurrenceStore isola o acesso a contratos de recorrência no banco (o *Store em
// prod, um fake nos testes), análogo a analyticsSource/chargeReader.
type recurrenceStore interface {
	CreateRecurrence(ctx context.Context, r *Recurrence) error
	GetRecurrence(ctx context.Context, id int64) (*Recurrence, error)
	ListRecurrences(ctx context.Context) ([]Recurrence, error)
	SetRecurrenceStatus(ctx context.Context, id int64, status string) error
	UpdateRecurrenceAuth(ctx context.Context, id int64, efiIDRec, brCode, qrCode, status string) error
	ListChargesByRecurrence(ctx context.Context, recurrenceID int64) ([]Charge, error)
}

type createRecurrenceInput struct {
	SubscriptionID int64  `json:"subscriptionId"`
	AmountCents    int64  `json:"amountCents"`
	PayerTaxID     string `json:"payerTaxId"`
	PayerName      string `json:"payerName"`
	DueDay         int    `json:"dueDay"`
	StartDate      string `json:"startDate"` // YYYY-MM-DD; default hoje
	EndDate        string `json:"endDate"`   // YYYY-MM-DD; opcional
}

// handleCreateRecurrence (admin) cria um contrato de PIX Automático de mensalidade
// (jornada 2: o QR/copia-e-cola só AUTORIZA; o 1º débito cai no próximo ciclo).
func (s *Server) handleCreateRecurrence(w http.ResponseWriter, r *http.Request) {
	var in createRecurrenceInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.PayerTaxID = onlyDigits(in.PayerTaxID)
	in.PayerName = strings.TrimSpace(in.PayerName)
	if in.SubscriptionID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "subscriptionId obrigatório")
		return
	}
	if in.AmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "amountCents deve ser > 0")
		return
	}
	if !validCPF(in.PayerTaxID) {
		writeError(w, http.StatusBadRequest, "invalid_body", "CPF inválido (11 dígitos)")
		return
	}
	if !validName(in.PayerName) {
		writeError(w, http.StatusBadRequest, "invalid_body", "payerName obrigatório (máximo 100 caracteres)")
		return
	}
	// O PIX Automático debita no dia do vencimento; a Efí limita o dia a 1–28 para
	// evitar meses sem o dia 29/30/31 (espelha a regra das assinaturas).
	if in.DueDay < 1 || in.DueDay > 28 {
		writeError(w, http.StatusBadRequest, "invalid_body", "dueDay deve estar entre 1 e 28")
		return
	}
	start := strings.TrimSpace(in.StartDate)
	todayStr := time.Now().Format("2006-01-02")
	if start == "" {
		// dataInicial deve ser FUTURA (a Efí recusa data = criação); default = amanhã.
		start = time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	} else {
		if _, err := time.Parse("2006-01-02", start); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body", "startDate deve ser YYYY-MM-DD")
			return
		}
		// Comparação lexicográfica de YYYY-MM-DD = cronológica.
		if start <= todayStr {
			writeError(w, http.StatusBadRequest, "invalid_body", "startDate deve ser uma data futura")
			return
		}
	}
	end := strings.TrimSpace(in.EndDate)
	if end != "" {
		if _, err := time.Parse("2006-01-02", end); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body", "endDate deve ser YYYY-MM-DD")
			return
		}
	}

	subID := in.SubscriptionID
	dueDay := in.DueDay
	rec := &Recurrence{
		SubscriptionID: &subID,
		PayerTaxID:     in.PayerTaxID,
		PayerName:      in.PayerName,
		AmountCents:    in.AmountCents,
		Periodicity:    "MENSAL", // fase 1: mensalidade
		DueDay:         &dueDay,
		StartDate:      start,
		EndDate:        end,
		Journey:        2,
	}
	// Persiste primeiro (status 'pending_auth') para ter o ID; a FK valida a assinatura.
	if err := s.recs.CreateRecurrence(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao criar recorrência")
		return
	}

	res, err := s.efi.CreateRecurrence(r.Context(), RecurrenceRequest{
		// Contrato único por recorrência (a Efí rejeita contrato já com recorrência ativa);
		// usa o id da recorrência, não o da assinatura, p/ permitir recriar após cancelar.
		Contract:    "Assinatura #" + strconv.FormatInt(rec.ID, 10),
		Object:      "Mensalidade Santos Tech",
		PayerName:   rec.PayerName,
		PayerTaxID:  rec.PayerTaxID,
		AmountCents: rec.AmountCents,
		Periodicity: rec.Periodicity,
		StartDate:   rec.StartDate,
		EndDate:     rec.EndDate,
	})
	if err != nil {
		// 422 (não 502): o Cloudflare/Traefik troca 5xx do origin por HTML sem CORS.
		_ = s.recs.SetRecurrenceStatus(r.Context(), rec.ID, "rejected")
		var pe *ProviderError
		if errors.As(err, &pe) {
			slog.Warn("recorrência: erro do gateway", "status", pe.Status, "message", pe.Message)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("recorrência: falha no provider", "err", err)
		writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao criar a recorrência. Tente novamente.")
		return
	}
	if err := s.recs.UpdateRecurrenceAuth(r.Context(), rec.ID, res.EfiIDRec, res.BRCode, res.QRCode, res.Status); err != nil {
		// A recorrência já existe na Efí; registra para reconciliação mas não falha o request.
		slog.Warn("recorrência: falha ao gravar autorização", "rec", rec.ID, "err", err)
	}
	rec.EfiIDRec, rec.BRCode, rec.QRCode, rec.Status = res.EfiIDRec, res.BRCode, res.QRCode, res.Status

	writeJSON(w, http.StatusCreated, map[string]any{
		"id": rec.ID, "status": rec.Status, "brCode": rec.BRCode, "qrCode": rec.QRCode,
	})
}

// handleListRecurrences (admin) lista os contratos de recorrência.
func (s *Server) handleListRecurrences(w http.ResponseWriter, r *http.Request) {
	list, err := s.recs.ListRecurrences(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar recorrências")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGetRecurrence (admin) devolve o contrato + os ciclos (cobranças vinculadas).
func (s *Server) handleGetRecurrence(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_param", "id inválido")
		return
	}
	rec, err := s.recs.GetRecurrence(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Recorrência não encontrada")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao carregar recorrência")
		return
	}
	cycles, err := s.recs.ListChargesByRecurrence(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao carregar ciclos")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recurrence": rec, "cycles": cycles})
}

// handleCancelRecurrence (admin) cancela o contrato na Efí (best-effort) e localmente.
func (s *Server) handleCancelRecurrence(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_param", "id inválido")
		return
	}
	rec, err := s.recs.GetRecurrence(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Recorrência não encontrada")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao carregar recorrência")
		return
	}
	// Best-effort no gateway: se a Efí falhar, ainda cancelamos localmente (o ciclo
	// não será mais cobrado pelo loop, que só pega 'active').
	if rec.EfiIDRec != "" {
		if err := s.efi.CancelRecurrence(r.Context(), rec.EfiIDRec); err != nil {
			slog.Warn("recorrência: falha ao cancelar na Efí (segue cancelando local)", "rec", id, "err", err)
		}
	}
	if err := s.recs.SetRecurrenceStatus(r.Context(), id, "canceled"); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao cancelar recorrência")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "canceled"})
}
