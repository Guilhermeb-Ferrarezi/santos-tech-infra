package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeAnalytics struct{ got string }

func (f *fakeAnalytics) GetAnalytics(_ context.Context, r AnalyticsRange) (Analytics, error) {
	f.got = r.Key
	return Analytics{Range: r.Key, ByStatus: []Bucket{{Key: "paid", Total: 100, Count: 1}}}, nil
}

func TestHandleAnalytics(t *testing.T) {
	fa := &fakeAnalytics{}
	s := &Server{analytics: fa}
	r := httptest.NewRequest("GET", "/analytics?range=90d", nil)
	w := httptest.NewRecorder()
	s.handleAnalytics(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	if fa.got != "90d" {
		t.Fatalf("range deveria ser 90d, veio %q", fa.got)
	}
	var out Analytics
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if out.Range != "90d" || len(out.ByStatus) != 1 {
		t.Fatalf("payload inesperado: %+v", out)
	}
}
