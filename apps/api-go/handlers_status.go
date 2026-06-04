package main

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// svcStatus é o resultado da checagem de um serviço.
type svcStatus struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

func check(fn func(ctx context.Context) error) svcStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	err := fn(ctx)
	st := svcStatus{OK: err == nil, LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		st.Error = err.Error()
	}
	return st
}

func (s *Server) checkHTTP(url string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			return appErr(res.StatusCode, "UNHEALTHY", "status "+res.Status)
		}
		return nil
	}
}

// GET /status — saúde agregada do ecossistema (autenticado): banco e Redis do
// auth + health das APIs vizinhas (email e claude agent). Checagens em paralelo
// com timeout de 3s cada.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	results := map[string]svcStatus{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(name string, fn func(ctx context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := check(fn)
			mu.Lock()
			results[name] = st
			mu.Unlock()
		}()
	}

	run("db", func(ctx context.Context) error { return s.db.Ping(ctx) })
	run("redis", func(ctx context.Context) error { return s.rdb.Ping(ctx).Err() })
	run("email", s.checkHTTP(s.cfg.EmailAPIURL+"/health"))
	run("claude", s.checkHTTP(s.cfg.AgentURL+"/claude/health"))
	wg.Wait()

	overall := "ok"
	for _, st := range results {
		if !st.OK {
			overall = "degraded"
			break
		}
	}
	// auth = este próprio serviço: se respondeu, está de pé.
	results["auth"] = svcStatus{OK: true}
	writeJSON(w, http.StatusOK, map[string]any{"status": overall, "services": results})
}
