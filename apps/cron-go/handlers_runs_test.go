package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunJobBadID(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/cron/jobs/abc/run", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	s.handleRunJob(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400, veio %d", rec.Code)
	}
}
