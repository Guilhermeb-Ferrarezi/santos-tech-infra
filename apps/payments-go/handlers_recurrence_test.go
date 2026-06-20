package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// fakeRecStore implementa recurrenceStore para os testes unitários (sem Postgres).
type fakeRecStore struct {
	created    *Recurrence
	get        *Recurrence
	getErr     error
	list       []Recurrence
	listErr    error
	createErr  error
	statusSet  []string // status passados a SetRecurrenceStatus, em ordem
	authCalled bool
	cycles     []Charge
}

func (f *fakeRecStore) CreateRecurrence(_ context.Context, r *Recurrence) error {
	if f.createErr != nil {
		return f.createErr
	}
	r.ID = 1
	r.Status = "pending_auth"
	f.created = r
	return nil
}
func (f *fakeRecStore) GetRecurrence(_ context.Context, _ int64) (*Recurrence, error) {
	return f.get, f.getErr
}
func (f *fakeRecStore) ListRecurrences(_ context.Context) ([]Recurrence, error) {
	return f.list, f.listErr
}
func (f *fakeRecStore) SetRecurrenceStatus(_ context.Context, _ int64, status string) error {
	f.statusSet = append(f.statusSet, status)
	return nil
}
func (f *fakeRecStore) UpdateRecurrenceAuth(_ context.Context, _ int64, _, _, _, _ string) error {
	f.authCalled = true
	return nil
}
func (f *fakeRecStore) ListChargesByRecurrence(_ context.Context, _ int64) ([]Charge, error) {
	return f.cycles, nil
}

// As validações de POST /recurrences rodam ANTES de tocar o store/Efí, então cobrimos
// os caminhos 400 sem Postgres real (igual aos demais testes unitários do pacote).
func TestHandleCreateRecurrenceValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"json invalido", `{`},
		{"sem subscriptionId", `{"amountCents":5000,"payerTaxId":"39053344705","payerName":"Fulano","dueDay":10}`},
		{"amount zero", `{"subscriptionId":1,"amountCents":0,"payerTaxId":"39053344705","payerName":"Fulano","dueDay":10}`},
		{"cpf invalido", `{"subscriptionId":1,"amountCents":5000,"payerTaxId":"111","payerName":"Fulano","dueDay":10}`},
		{"sem nome", `{"subscriptionId":1,"amountCents":5000,"payerTaxId":"39053344705","payerName":"  ","dueDay":10}`},
		{"dueDay fora do range", `{"subscriptionId":1,"amountCents":5000,"payerTaxId":"39053344705","payerName":"Fulano","dueDay":31}`},
		{"startDate invalida", `{"subscriptionId":1,"amountCents":5000,"payerTaxId":"39053344705","payerName":"Fulano","dueDay":10,"startDate":"20-01-2026"}`},
	}
	s := &Server{efi: fakeEfiOps{}, recs: &fakeRecStore{}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/recurrences", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			s.handleCreateRecurrence(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("esperado 400, veio %d: %s", w.Code, w.Body.String())
			}
			var out struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if out.Code != "invalid_body" {
				t.Fatalf("code esperado invalid_body, veio %q", out.Code)
			}
		})
	}
}

// Caminho feliz: validações passam, store persiste, Efí autoriza → 201 com brCode/qrCode.
func TestHandleCreateRecurrenceHappy(t *testing.T) {
	st := &fakeRecStore{}
	s := &Server{efi: fakeEfiOps{}, recs: st}
	body := `{"subscriptionId":7,"amountCents":5000,"payerTaxId":"39053344705","payerName":"Fulano de Tal","dueDay":10}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/recurrences", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.handleCreateRecurrence(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201, veio %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		BRCode string `json:"brCode"`
		QRCode string `json:"qrCode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != 1 {
		t.Fatalf("id esperado 1, veio %d", out.ID)
	}
	// fakeEfiOps.CreateRecurrence devolve esses valores.
	if out.Status != "pending_auth" || out.BRCode != "00020101br-code" || out.QRCode == "" {
		t.Fatalf("resposta inesperada: %+v", out)
	}
	if !st.authCalled {
		t.Fatal("esperava UpdateRecurrenceAuth ter sido chamado após autorizar na Efí")
	}
	if st.created == nil || st.created.Periodicity != "MENSAL" || st.created.Journey != 2 {
		t.Fatalf("recorrência persistida inesperada: %+v", st.created)
	}
}

// Efí falha ao criar → status local vira 'rejected' e o handler responde 422.
func TestHandleCreateRecurrenceProviderError(t *testing.T) {
	st := &fakeRecStore{}
	s := &Server{efi: failingEfiOps{}, recs: st}
	body := `{"subscriptionId":7,"amountCents":5000,"payerTaxId":"39053344705","payerName":"Fulano de Tal","dueDay":10}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/recurrences", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.handleCreateRecurrence(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("esperado 422, veio %d: %s", w.Code, w.Body.String())
	}
	if len(st.statusSet) != 1 || st.statusSet[0] != "rejected" {
		t.Fatalf("esperava SetRecurrenceStatus('rejected'), veio %v", st.statusSet)
	}
}

func TestHandleListRecurrences(t *testing.T) {
	st := &fakeRecStore{list: []Recurrence{{ID: 1, Status: "active"}, {ID: 2, Status: "pending_auth"}}}
	s := &Server{recs: st}
	w := httptest.NewRecorder()
	s.handleListRecurrences(w, httptest.NewRequest("GET", "/recurrences", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d", w.Code)
	}
	var out []Recurrence
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 || out[0].ID != 1 {
		t.Fatalf("lista inesperada: %+v", out)
	}
}

func TestHandleGetRecurrenceBadID(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/recurrences/x", nil)
	req.SetPathValue("id", "x")
	s.handleGetRecurrence(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, veio %d", w.Code)
	}
}

func TestHandleGetRecurrenceNotFound(t *testing.T) {
	s := &Server{recs: &fakeRecStore{getErr: pgx.ErrNoRows}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/recurrences/99", nil)
	req.SetPathValue("id", "99")
	s.handleGetRecurrence(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("esperado 404, veio %d", w.Code)
	}
}

func TestHandleGetRecurrenceHappy(t *testing.T) {
	st := &fakeRecStore{
		get:    &Recurrence{ID: 3, Status: "active"},
		cycles: []Charge{{Status: "paid"}, {Status: "pending"}},
	}
	s := &Server{recs: st}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/recurrences/3", nil)
	req.SetPathValue("id", "3")
	s.handleGetRecurrence(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Recurrence Recurrence `json:"recurrence"`
		Cycles     []Charge   `json:"cycles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Recurrence.ID != 3 || len(out.Cycles) != 2 {
		t.Fatalf("detalhe inesperado: %+v", out)
	}
}

// Cancelar: contrato com efiIdRec → chama a Efí e marca 'canceled' local.
func TestHandleCancelRecurrenceHappy(t *testing.T) {
	st := &fakeRecStore{get: &Recurrence{ID: 5, Status: "active", EfiIDRec: "rec-1"}}
	s := &Server{recs: st, efi: fakeEfiOps{}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/recurrences/5/cancel", nil)
	req.SetPathValue("id", "5")
	s.handleCancelRecurrence(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	if len(st.statusSet) != 1 || st.statusSet[0] != "canceled" {
		t.Fatalf("esperava SetRecurrenceStatus('canceled'), veio %v", st.statusSet)
	}
}

// Best-effort: a Efí falha ao cancelar, mas o cancelamento LOCAL prossegue (200).
func TestHandleCancelRecurrenceEfiFailsBestEffort(t *testing.T) {
	st := &fakeRecStore{get: &Recurrence{ID: 6, Status: "active", EfiIDRec: "rec-2"}}
	s := &Server{recs: st, efi: failingEfiOps{}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/recurrences/6/cancel", nil)
	req.SetPathValue("id", "6")
	s.handleCancelRecurrence(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Efí falhando não deveria impedir o cancelamento local; esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	if len(st.statusSet) != 1 || st.statusSet[0] != "canceled" {
		t.Fatalf("esperava cancelamento local mesmo com Efí falhando, veio %v", st.statusSet)
	}
}

func TestHandleRecWebhookSegredoErrado(t *testing.T) {
	s := &Server{cfg: Config{Production: true, EFIWebhookSecret: "right"}, efi: fakeEfiOps{}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/webhooks/efi/rec?token=wrong", strings.NewReader(`{"rec":[]}`))
	s.handleRecWebhook(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("segredo errado deveria dar 401, veio %d", w.Code)
	}
}

func TestHandleRecWebhookSegredoCerto(t *testing.T) {
	// store nil → 200 cedo (após autenticar e fazer o parse). Cobre o caminho feliz de auth+parse.
	s := &Server{cfg: Config{EFIWebhookSecret: "right"}, efi: fakeEfiOps{}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/webhooks/efi/rec?token=right", strings.NewReader(`{"rec":[{"idRec":"rec-fake-1","status":"APROVADA"}]}`))
	s.handleRecWebhook(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("segredo certo deveria dar 200, veio %d: %s", w.Code, w.Body.String())
	}
}

// failingEfiOps é um efiOps cujas operações de recorrência falham (best-effort/erro).
type failingEfiOps struct{ fakeEfiOps }

func (failingEfiOps) CreateRecurrence(_ context.Context, _ RecurrenceRequest) (RecurrenceResult, error) {
	return RecurrenceResult{}, errors.New("efi indisponível")
}
func (failingEfiOps) CancelRecurrence(_ context.Context, _ string) error {
	return errors.New("efi indisponível")
}
