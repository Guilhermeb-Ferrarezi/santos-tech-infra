package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	qrcode "github.com/skip2/go-qrcode"
)

type payDTO struct {
	AmountCents int64  `json:"amountCents"`
	Method      string `json:"method"` // pix | boleto
	BRCode      string `json:"brCode"` // pix: copia-e-cola · boleto: linha digitável
	QRCode      string `json:"qrCode"`
	PDFURL      string `json:"pdfUrl,omitempty"`  // boleto: link do PDF
	Barcode     string `json:"barcode,omitempty"` // boleto: código de barras
	Status      string `json:"status"`
	DueDate     string `json:"dueDate"`
}

func (s *Server) handleGetPay(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetChargeByPublicToken(r.Context(), r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	writeJSON(w, http.StatusOK, payDTO{
		AmountCents: c.AmountCents, Method: methodOrPix(c.Method), BRCode: c.BRCode, QRCode: c.QRCode,
		PDFURL: c.PDFURL, Barcode: c.Barcode, Status: c.Status, DueDate: c.DueDate,
	})
}

// handlePayQR serve o PNG do QR Code de uma cobrança PIX pelo token público — usado
// para EMBUTIR o QR no e-mail de cobrança (data-uri é bloqueado pelo Gmail; precisa ser
// uma URL hospedada). Só PIX; boleto não tem QR.
func (s *Server) handlePayQR(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetChargeByPublicToken(r.Context(), r.PathValue("token"))
	if err != nil || methodOrPix(c.Method) != "pix" || c.BRCode == "" {
		writeError(w, http.StatusNotFound, "not_found", "QR não disponível")
		return
	}
	png, err := qrcode.Encode(c.BRCode, qrcode.Medium, 360)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "qr_error", "Falha ao gerar QR")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(png) //nolint:errcheck
}

// SSE: envia o status atual e, quando o webhook publicar "paid", empurra e encerra.
func (s *Server) handlePayEvents(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	status, err := s.chargeStatus(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	// startSSE remove o write deadline desta conexão: o WriteTimeout de 60s do
	// servidor vale para a resposta INTEIRA e matava o stream antes do 'paid'.
	flusher, ok := startSSE(w)
	if !ok {
		writeError(w, http.StatusInternalServerError, "sse_unsupported", "Streaming indisponível")
		return
	}

	// estado atual já resolve quem chega depois do pagamento
	fmt.Fprintf(w, "event: status\ndata: %s\n\n", status)
	flusher.Flush()
	if status != "pending" {
		return
	}

	pubsub := s.subscribeCharge(r.Context(), token)
	defer pubsub.Close()
	ch := pubsub.Channel()

	// Re-checa após assinar: fecha a janela de corrida em que o pagamento cai
	// ENTRE o GetCharge inicial e o Subscribe (senão o evento publicado se perderia
	// e o stream ficaria aberto pra sempre). Re-checa direto no banco (ignora cache)
	// para não confiar num valor "pending" potencialmente defasado nesta corrida.
	if c2, err := s.store.GetChargeByPublicToken(r.Context(), token); err == nil && c2.Status != "pending" {
		s.cacheChargeStatus(r.Context(), token, c2.Status)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", c2.Status, c2.Status)
		flusher.Flush()
		return
	}

	// Ping periódico: sem tráfego, proxies derrubam a conexão ociosa antes do evento.
	ping := time.NewTicker(sseKeepAliveInterval)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			sseKeepAlive(w, flusher)
		case msg := <-ch:
			// Qualquer evento terminal publicado (paid/canceled) encerra o stream.
			if msg != nil && msg.Payload != "" {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Payload, msg.Payload)
				flusher.Flush()
				return
			}
		}
	}
}

// handleCancelPay cancela uma cobrança pendente pelo token público (botão na tela de
// pagamento). Marca como cancelada no banco, tenta remover a cob na Efí (best-effort,
// pois o gateway expira sozinho) e avisa os streams SSE inscritos.
func (s *Server) handleCancelPay(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	correlationID, providerChargeID, method, err := s.store.CancelChargeByToken(r.Context(), token)
	if errors.Is(err, pgx.ErrNoRows) {
		// Já paga, já cancelada/expirada, ou token inexistente: nada a cancelar.
		writeError(w, http.StatusConflict, "not_cancelable", "Cobrança não pode ser cancelada")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao cancelar")
		return
	}
	// Remove a cobrança na Efí na hora (best-effort): se falhar, ela ainda expira sozinha.
	if method == "boleto" {
		if s.efiCobr != nil && providerChargeID != "" {
			if e := s.efiCobr.CancelBoleto(r.Context(), providerChargeID); e != nil {
				slog.Warn("falha ao cancelar boleto na Efí (segue cancelado localmente)", "corr", correlationID, "err", e)
			}
		}
	} else if s.efi != nil {
		id := providerChargeID
		if id == "" {
			id = correlationID
		}
		if e := s.efi.CancelCharge(r.Context(), id); e != nil {
			slog.Warn("falha ao cancelar cob na Efí (segue cancelada localmente)", "corr", correlationID, "err", e)
		}
	}
	s.invalidateChargeStatus(r.Context(), token)
	s.publishChargeCanceled(r.Context(), token)
	writeJSON(w, http.StatusOK, map[string]string{"status": "canceled"})
}

// handlePayReceipt entrega o comprovante PDF do Pix ao próprio pagador, pelo token
// público da cobrança (sem login). Só para cobranças já pagas.
func (s *Server) handlePayReceipt(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	c, err := s.store.GetChargeByPublicToken(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	if c.Status != "paid" {
		writeError(w, http.StatusConflict, "not_paid", "Cobrança ainda não foi paga")
		return
	}
	if methodOrPix(c.Method) == "boleto" {
		// Boleto não tem o comprovante Pix (/gn/pix/comprovantes); o próprio PDF do boleto
		// serve de referência na tela de pagamento.
		writeError(w, http.StatusConflict, "receipt_unavailable", "Comprovante em PDF disponível apenas para PIX")
		return
	}
	if s.efi == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Comprovante indisponível")
		return
	}
	ct, body, err := s.efi.GetReceipt(r.Context(), c.CorrelationID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao obter comprovante na Efí")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="comprovante.pdf"`)
	w.Write(body) //nolint:errcheck
}
