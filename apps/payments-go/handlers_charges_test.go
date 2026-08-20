package main

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeChargeWriter registra a ORDEM das operações de persistência da cobrança.
type fakeChargeWriter struct {
	mu        sync.Mutex
	ops       []string
	nextID    int64
	insertErr error
	activErr  error
	lastState *Charge
}

func (f *fakeChargeWriter) InsertCharge(_ context.Context, c *Charge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "insert:"+c.Status)
	if f.insertErr != nil {
		return f.insertErr
	}
	f.nextID++
	c.ID = f.nextID
	cp := *c
	f.lastState = &cp
	return nil
}

func (f *fakeChargeWriter) ActivateCharge(_ context.Context, id int64, c *Charge) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "activate")
	if f.activErr != nil {
		return false, f.activErr
	}
	cp := *c
	cp.ID = id
	cp.Status = "pending"
	f.lastState = &cp
	return true, nil
}

func (f *fakeChargeWriter) AbandonCharge(_ context.Context, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "abandon")
	return nil
}

func (f *fakeChargeWriter) opList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

// providerSpy registra se CreateCharge foi chamado e pode falhar sob demanda.
type providerSpy struct {
	calls    int
	failWith error
	onCall   func()
}

func (p *providerSpy) CreateCharge(_ context.Context, req ChargeRequest) (ChargeResult, error) {
	p.calls++
	if p.onCall != nil {
		p.onCall()
	}
	if p.failWith != nil {
		return ChargeResult{}, p.failWith
	}
	return ChargeResult{
		ProviderChargeID: "txid-123",
		CorrelationID:    req.CorrelationID,
		BRCode:           "brcode-123",
	}, nil
}

func (p *providerSpy) GetCharge(_ context.Context, _ string) (ChargeResult, error) {
	return ChargeResult{}, nil
}

func (p *providerSpy) ParseWebhook(_ map[string][]string, _ []byte) ([]WebhookEvent, error) {
	return nil, nil
}

func chargeFixture() *Charge {
	return &Charge{
		Kind: "avulso", AmountCents: 5000, DueDate: "2026-09-01",
		Provider: "efi", CorrelationID: "corr-fix", PublicToken: "tok-fix",
	}
}

// TestCreateCharge_ReservaAntesDoGateway é a regressão central do item: a cobrança
// PRECISA existir no banco antes de existir na Efí. Na ordem antiga (Efí primeiro),
// um INSERT que falhasse deixava um QR pagável sem cobrança nenhuma do nosso lado —
// o cliente pagava e o webhook não tinha o que casar.
func TestCreateCharge_ReservaAntesDoGateway(t *testing.T) {
	wr := &fakeChargeWriter{}
	prov := &providerSpy{}
	// No momento em que o gateway é chamado, a reserva já tem de estar gravada.
	prov.onCall = func() {
		if got := wr.opList(); len(got) != 1 || got[0] != "insert:creating" {
			t.Errorf("a Efí foi chamada antes da reserva; ops=%v", got)
		}
	}
	s := &Server{chargeWr: wr, provider: prov}

	c := chargeFixture()
	if err := s.createAndPersistCharge(context.Background(), c, &Student{Name: "Ana", TaxID: "12345678909"}, "Teste"); err != nil {
		t.Fatalf("createAndPersistCharge: %v", err)
	}

	want := []string{"insert:creating", "activate"}
	got := wr.opList()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ordem esperada %v, veio %v", want, got)
	}
	if c.Status != "pending" {
		t.Fatalf("cobrança deveria terminar 'pending', veio %q", c.Status)
	}
	if c.ProviderChargeID != "txid-123" || c.BRCode != "brcode-123" {
		t.Fatalf("dados do gateway não foram aplicados: %+v", c)
	}
}

// TestCreateCharge_GatewayFalhaCancelaReserva: se a Efí recusa, a reserva não pode
// ficar como cobrança pendente fantasma.
func TestCreateCharge_GatewayFalhaCancelaReserva(t *testing.T) {
	wr := &fakeChargeWriter{}
	prov := &providerSpy{failWith: errors.New("efi indisponível")}
	s := &Server{chargeWr: wr, provider: prov}

	err := s.createAndPersistCharge(context.Background(), chargeFixture(), &Student{Name: "Ana", TaxID: "12345678909"}, "Teste")
	if err == nil {
		t.Fatal("esperava erro do gateway")
	}
	want := []string{"insert:creating", "abandon"}
	got := wr.opList()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ordem esperada %v, veio %v", want, got)
	}
}

// TestCreateCharge_InsertFalhaNaoChamaGateway: sem conseguir reservar, nem chega a
// existir um QR pagável — que é exatamente o cenário que causava o prejuízo.
func TestCreateCharge_InsertFalhaNaoChamaGateway(t *testing.T) {
	wr := &fakeChargeWriter{insertErr: errors.New("db fora")}
	prov := &providerSpy{}
	s := &Server{chargeWr: wr, provider: prov}

	if err := s.createAndPersistCharge(context.Background(), chargeFixture(), &Student{Name: "Ana", TaxID: "12345678909"}, "Teste"); err == nil {
		t.Fatal("esperava erro do banco")
	}
	if prov.calls != 0 {
		t.Fatalf("o gateway NÃO deveria ter sido chamado, foram %d chamadas", prov.calls)
	}
}
