package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildLogsExtractsOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"commit":"abc1234","commit_message":"quebrou","logs":"[{\"output\":\"npm ERR\"},{\"output\":\"exit 1\"}]"}`))
	}))
	defer srv.Close()
	c := NewCoolifyClient(srv.URL, "t")
	commit, msg, err := c.DeploymentInfo(context.Background(), "d1")
	if err != nil || commit != "abc1234" || msg != "quebrou" {
		t.Fatalf("commit=%q msg=%q err=%v", commit, msg, err)
	}
	logs, err := c.BuildLogs(context.Background(), "d1")
	if err != nil || logs != "npm ERR\nexit 1" {
		t.Fatalf("logs=%q err=%v", logs, err)
	}
}

func TestAppRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"git_repository":"https://github.com/org/r.git","git_branch":"main"}`))
	}))
	defer srv.Close()
	c := NewCoolifyClient(srv.URL, "t")
	repo, branch, err := c.AppRepo(context.Background(), "a1")
	if err != nil || repo != "https://github.com/org/r.git" || branch != "main" {
		t.Fatalf("repo=%q branch=%q err=%v", repo, branch, err)
	}
}
