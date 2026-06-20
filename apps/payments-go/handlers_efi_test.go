package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeEfiOps struct {
	reportStatus string // "done" (default) ou "processing"
}

func (f fakeEfiOps) GetBalance(context.Context) (int64, error)      { return 12345, nil }
func (f fakeEfiOps) CancelCharge(_ context.Context, _ string) error { return nil }
func (f fakeEfiOps) GetReceipt(_ context.Context, _ string) (string, []byte, error) {
	return "application/pdf", []byte("%PDF-1.4 fake"), nil
}
func (f fakeEfiOps) ListMED(_ context.Context, _, _ time.Time) ([]MEDInfraction, error) {
	return []MEDInfraction{
		{ID: "inf001", EndToEndID: "E00000000", Status: "pendente", Razao: "razao teste", ValueCents: 1500, DataTransacao: "2025-01-01T00:00:00Z"},
	}, nil
}
func (f fakeEfiOps) RequestReport(_ context.Context, _ string) (string, error) { return "rep-1", nil }
func (f fakeEfiOps) GetReport(_ context.Context, _ string) (string, string, []byte, error) {
	status := f.reportStatus
	if status == "" {
		status = "done"
	}
	if status == "processing" {
		return "processing", "", nil, nil
	}
	return "done", "text/csv", []byte("a,b\n1,2"), nil
}
func (f fakeEfiOps) CreateRecurrence(_ context.Context, _ RecurrenceRequest) (RecurrenceResult, error) {
	return RecurrenceResult{EfiIDRec: "rec-fake-1", BRCode: "00020101br-code", QRCode: "data:image/png;base64,fake", Status: "pending_auth"}, nil
}
func (f fakeEfiOps) CreateRecurrenceJornada3(_ context.Context, req RecurrenceJornada3Request) (RecurrenceResult, ChargeResult, error) {
	return RecurrenceResult{EfiIDRec: "rec-fake-j3", BRCode: "00020101br-code-j3", QRCode: "data:image/png;base64,j3", Status: "pending_auth"},
		ChargeResult{ProviderChargeID: req.ChargeCorrelationID, BRCode: "00020101br-code-j3", Status: "pending"}, nil
}
func (f fakeEfiOps) GetRecurrence(_ context.Context, _ string) (string, error) { return "active", nil }
func (f fakeEfiOps) CancelRecurrence(_ context.Context, _ string) error        { return nil }
func (f fakeEfiOps) CreateRecurringCharge(_ context.Context, _ RecurringChargeRequest) (ChargeResult, error) {
	return ChargeResult{ProviderChargeID: "stpaycycle1", BRCode: "00020101cobr", Status: "pending"}, nil
}
func (f fakeEfiOps) ParseRecWebhook(_ map[string][]string, body []byte) ([]RecEvent, error) {
	return []RecEvent{{EfiIDRec: "rec-fake-1", Status: "active", Raw: body}}, nil
}
func (f fakeEfiOps) RegisterRecWebhook(_ context.Context, _ string) error { return nil }

// fakeChargeReader implementa chargeReader para testes unitários.
type fakeChargeReader struct {
	charge *Charge
	err    error
}

func (f fakeChargeReader) GetCharge(_ context.Context, _ int64) (*Charge, error) {
	return f.charge, f.err
}

func TestHandleEfiBalance(t *testing.T) {
	s := &Server{efi: fakeEfiOps{}}
	w := httptest.NewRecorder()
	s.handleEfiBalance(w, httptest.NewRequest("GET", "/efi/balance", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	var out struct {
		AvailableCents int64 `json:"availableCents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.AvailableCents != 12345 {
		t.Fatalf("availableCents=%d", out.AvailableCents)
	}
}

// TestHandleReceiptNotFound: chargeReader retorna (nil, nil) → 404 not_found.
func TestHandleReceiptNotFound(t *testing.T) {
	s := &Server{
		charges: fakeChargeReader{charge: nil, err: nil},
		efi:     fakeEfiOps{},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/charges/99/receipt", nil)
	req.SetPathValue("id", "99")
	s.handleReceipt(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("esperado 404, veio %d", w.Code)
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Code != "not_found" {
		t.Fatalf("code esperado not_found, veio %q", out.Code)
	}
}

// TestHandleReceiptNotPaid: cobrança com status "pending" → 409 not_paid.
func TestHandleReceiptNotPaid(t *testing.T) {
	s := &Server{
		charges: fakeChargeReader{charge: &Charge{Status: "pending"}, err: nil},
		efi:     fakeEfiOps{},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/charges/42/receipt", nil)
	req.SetPathValue("id", "42")
	s.handleReceipt(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("esperado 409, veio %d", w.Code)
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Code != "not_paid" {
		t.Fatalf("code esperado not_paid, veio %q", out.Code)
	}
}

// TestHandleReceiptHappy: cobrança paid + GetReceipt retorna PDF → 200 application/pdf.
func TestHandleReceiptHappy(t *testing.T) {
	s := &Server{
		charges: fakeChargeReader{
			charge: &Charge{Status: "paid", CorrelationID: "stpayX"},
			err:    nil,
		},
		efi: fakeEfiOps{},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/charges/7/receipt", nil)
	req.SetPathValue("id", "7")
	s.handleReceipt(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/pdf" {
		t.Fatalf("Content-Type esperado application/pdf, veio %q", ct)
	}
	if w.Body.Len() == 0 {
		t.Fatal("body vazio, esperado conteúdo PDF")
	}
}

// TestHandleReportRequest: POST /efi/reports com {date} → 200 {reportId:"rep-1"}.
func TestHandleReportRequest(t *testing.T) {
	s := &Server{efi: fakeEfiOps{}}
	body := strings.NewReader(`{"date":"2025-06-18"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/efi/reports", body)
	req.Header.Set("Content-Type", "application/json")
	s.handleReportRequest(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		ReportID string `json:"reportId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ReportID != "rep-1" {
		t.Fatalf("reportId esperado rep-1, veio %q", out.ReportID)
	}
}

// TestHandleReportGetDone: GET /efi/reports/{id} quando status="done" → 200 text/csv.
func TestHandleReportGetDone(t *testing.T) {
	s := &Server{efi: fakeEfiOps{}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/efi/reports/rep-1", nil)
	req.SetPathValue("id", "rep-1")
	s.handleReportGet(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if ct != "text/csv" {
		t.Fatalf("Content-Type esperado text/csv, veio %q", ct)
	}
	if w.Body.Len() == 0 {
		t.Fatal("body vazio, esperado CSV")
	}
}

// TestHandleReportGetProcessing: GET /efi/reports/{id} quando status="processing" → 200 {"status":"processing"}.
func TestHandleReportGetProcessing(t *testing.T) {
	s := &Server{efi: fakeEfiOps{reportStatus: "processing"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/efi/reports/rep-proc", nil)
	req.SetPathValue("id", "rep-proc")
	s.handleReportGet(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Status != "processing" {
		t.Fatalf("status esperado processing, veio %q", out.Status)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("Content-Type esperado application/json, veio %q", ct)
	}
}

// TestHandleReportRequestBadDate: POST com date inválido → 400 invalid_request.
func TestHandleReportRequestBadDate(t *testing.T) {
	s := &Server{efi: fakeEfiOps{}}
	body := strings.NewReader(`{"date":"nao-e-data"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/efi/reports", body)
	req.Header.Set("Content-Type", "application/json")
	s.handleReportRequest(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, veio %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Code != "invalid_request" {
		t.Fatalf("code esperado invalid_request, veio %q", out.Code)
	}
}

// TestHandleMED: fake retorna 1 infração; espera 200 e array com 1 item.
func TestHandleMED(t *testing.T) {
	s := &Server{efi: fakeEfiOps{}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/efi/med?range=30d", nil)
	s.handleEfiMED(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out []MEDInfraction
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("esperado 1 infração, veio %d", len(out))
	}
	if out[0].ID != "inf001" {
		t.Fatalf("ID esperado inf001, veio %q", out[0].ID)
	}
	if out[0].ValueCents != 1500 {
		t.Fatalf("ValueCents esperado 1500, veio %d", out[0].ValueCents)
	}
}
