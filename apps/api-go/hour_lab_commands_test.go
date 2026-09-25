package main

import (
	"context"
	"fmt"
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
