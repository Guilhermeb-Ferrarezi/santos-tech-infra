package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateStudent_Validacao(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/students", strings.NewReader(`{"name":"","taxId":"","email":""}`))
	s.handleCreateStudent(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400 para payload inválido, veio %d", rec.Code)
	}
}

func TestCreatePlan_Validacao(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/plans", strings.NewReader(`{"name":"Mensal","amountCents":0,"dueDay":10}`))
	s.handleCreatePlan(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400, veio %d", rec.Code)
	}
}

func TestCreateCharge_Validacao(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/charges", strings.NewReader(`{"kind":"avulso","amountCents":0}`))
	s.handleCreateCharge(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400, veio %d", rec.Code)
	}
}

func TestWebhook_Responde200SemStore(t *testing.T) {
	s := &Server{provider: &dotfyProvider{}}
	rec := httptest.NewRecorder()
	body := `{"event":"CHARGE_PAID","data":{"id":"ch_1","correlationID":"stpay_abc"}}`
	req := httptest.NewRequest("POST", "/webhooks/dotfy", strings.NewReader(body))
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handler panicou: %v", r)
		}
	}()
	s.handleDotfyWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", rec.Code)
	}
}
