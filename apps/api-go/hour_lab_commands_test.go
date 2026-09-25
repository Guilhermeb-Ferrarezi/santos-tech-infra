package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeLabCmds implementa labCommandStore em memória com a MESMA semântica do
// SQL: fila por device, entrega do mais antigo, uma vez só.
type fakeLabCmds struct {
	mu       sync.Mutex
	pending  map[string][]LabCommand // deviceUUID -> fila
	enqueued []fakeEnqueued
	audits   []fakeEnqueued
	results  map[string]string
	entries  map[string]*LabCommandEntry
	failNext error
}

type fakeEnqueued struct {
	DeviceID, Source, Kind, Text string
	UserID                       int64
}

func newFakeLabCmds() *fakeLabCmds {
	return &fakeLabCmds{pending: map[string][]LabCommand{}, results: map[string]string{}, entries: map[string]*LabCommandEntry{}}
}

func (f *fakeLabCmds) Enqueue(_ context.Context, deviceID string, userID int64, source, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return "", err
	}
	id := fmt.Sprintf("11111111-1111-1111-1111-%012d", len(f.enqueued)+1)
	f.enqueued = append(f.enqueued, fakeEnqueued{deviceID, source, "command", text, userID})
	return id, nil
}

func (f *fakeLabCmds) Audit(_ context.Context, deviceID string, userID int64, source, kind, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, fakeEnqueued{deviceID, source, kind, text, userID})
	return nil
}

func (f *fakeLabCmds) push(deviceUUID string, c LabCommand) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending[deviceUUID] = append(f.pending[deviceUUID], c)
}

func (f *fakeLabCmds) ClaimNext(_ context.Context, deviceUUID string) (*LabCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.pending[deviceUUID]
	if len(q) == 0 {
		return nil, nil
	}
	c := q[0]
	f.pending[deviceUUID] = q[1:]
	return &c, nil
}

func (f *fakeLabCmds) StoreResult(_ context.Context, _, commandID, result string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.results[commandID]; ok {
		return false, nil
	}
	f.results[commandID] = result
	return true, nil
}

func (f *fakeLabCmds) List(_ context.Context, _ string, _ int32) ([]LabCommandEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []LabCommandEntry{}
	for _, e := range f.entries {
		out = append(out, *e)
	}
	return out, nil
}

func (f *fakeLabCmds) Get(_ context.Context, _, commandID string) (*LabCommandEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entries[commandID], nil
}

var _ labCommandStore = (*fakeLabCmds)(nil)
var _ labCommandStore = sqlLabCommandStore{}

func TestFakeClaimNextEntregaUmaVezSo(t *testing.T) {
	f := newFakeLabCmds()
	f.push("pc-1", LabCommand{ID: "a", Text: "hostname"})
	c1, _ := f.ClaimNext(context.Background(), "pc-1")
	c2, _ := f.ClaimNext(context.Background(), "pc-1")
	if c1 == nil || c1.ID != "a" || c2 != nil {
		t.Fatalf("c1=%v c2=%v", c1, c2)
	}
}

func TestShouldDeliverCommand(t *testing.T) {
	cases := []struct {
		app  string
		wd   bool
		want bool
	}{
		{"wnsh-watchdog", false, true},
		{"wnsh-watchdog", true, true},
		{"0.1.12", true, false}, // app + watchdog no mesmo PC: só o watchdog executa
		{"0.1.12", false, true}, // PC só com o app continua recebendo
	}
	for _, c := range cases {
		if got := shouldDeliverCommand(c.app, c.wd); got != c.want {
			t.Errorf("app=%s wd=%v: got %v", c.app, c.wd, got)
		}
	}
}

func TestRequestTokenSource(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer st_abc")
	if got := requestTokenSource(r, "x"); got != "pat" {
		t.Fatalf("PAT: %s", got)
	}
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(&http.Cookie{Name: "access_token", Value: "qualquer"})
	if got := requestTokenSource(r2, "x"); got != "painel" {
		t.Fatalf("cookie: %s", got)
	}
}

func TestHeartbeatSoEntregaProWatchdogQuandoHaWatchdog(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	f := newFakeLabCmds()
	s.labCmds = f
	f.push("pc-1", LabCommand{ID: "a", Text: "hostname"})
	// watchdog bate primeiro: marca presença e leva o comando
	if c := s.nextLabCommandForHeartbeat(context.Background(), "pc-1", watchdogAppVersion); c == nil || c.ID != "a" {
		t.Fatalf("watchdog deveria receber: %v", c)
	}
	f.push("pc-1", LabCommand{ID: "b", Text: "whoami"})
	// app do usuário no mesmo PC: não recebe
	if c := s.nextLabCommandForHeartbeat(context.Background(), "pc-1", "0.1.12"); c != nil {
		t.Fatalf("app não deveria receber com watchdog presente: %v", c)
	}
	// e o comando continua lá pro watchdog
	if c := s.nextLabCommandForHeartbeat(context.Background(), "pc-1", watchdogAppVersion); c == nil || c.ID != "b" {
		t.Fatalf("watchdog deveria receber b: %v", c)
	}
}

func TestSendCommandEnfileiraComUsuarioEOrigem(t *testing.T) {
	s := testServer(Config{JWTSecret: "x"})
	f := newFakeLabCmds()
	s.labCmds = f
	id := "2a502d12-676b-4723-80ae-b65335514df9"
	r := httptest.NewRequest("POST", "/hour-lab-devices/"+id+"/command", strings.NewReader(`{"text":"hostname"}`))
	r.SetPathValue("id", id)
	r.Header.Set("Authorization", "Bearer st_pat")
	w := httptest.NewRecorder()
	s.handleSendLabDeviceCommand(w, reqAs(r, 55))
	if w.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
	if len(f.enqueued) != 1 || f.enqueued[0].UserID != 55 || f.enqueued[0].Source != "pat" || f.enqueued[0].DeviceID != id {
		t.Fatalf("enfileirado errado: %+v", f.enqueued)
	}
	var out struct{ CommandID string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.CommandID == "" {
		t.Fatalf("sem commandId: %s", w.Body)
	}
}

func TestSendCommandDeviceInexistente404(t *testing.T) {
	s := testServer(Config{})
	f := newFakeLabCmds()
	f.failNext = errLabDeviceNotFound
	s.labCmds = f
	id := "2a502d12-676b-4723-80ae-b65335514df9"
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"x"}`))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	s.handleSendLabDeviceCommand(w, reqAs(r, 55))
	if w.Code != http.StatusNotFound {
		t.Fatalf("code=%d", w.Code)
	}
}
