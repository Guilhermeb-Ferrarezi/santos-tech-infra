package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
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
		_, _ = io.Copy(io.Discard, res.Body) // drain so the connection can be reused
		if res.StatusCode >= 400 {
			return appErr(res.StatusCode, "UNHEALTHY", "status "+res.Status)
		}
		return nil
	}
}

// GET /status — saúde agregada do ecossistema (autenticado): banco e Redis do
// auth + health das APIs vizinhas (email e claude agent). Checagens em paralelo
// com timeout de 3s cada.
//
// A rota é authGuard (qualquer usuário logado), não adminGuard — então o
// detalhe de erro (err.Error(), que pode vazar host/porta/DSN de Postgres,
// Redis ou das APIs vizinhas) só é incluído na resposta para admin. Usuários
// não-admin ainda veem ok/latencyMs por serviço, só sem o texto do erro
// (mesmo espírito do /ready, que nunca expõe err.Error()).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	isAdmin := s.requesterIsAdmin(r)
	results := map[string]svcStatus{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(name string, fn func(ctx context.Context) error) {
		wg.Add(1)
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("panic em checagem de status", "service", name, "panic", rec, "stack", string(debug.Stack()))
				}
			}()
			defer wg.Done()
			st := check(fn)
			if !isAdmin {
				st.Error = ""
			}
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

// GET /ready — readiness probe PÚBLICA (sem auth, fora do rate limit): pinga as
// dependências críticas (Postgres + Redis) com timeout curto e responde 503 se
// alguma falhar. É o que o orquestrador usa pra decidir mandar tráfego. Diferente
// de /health (liveness, não toca dependências).
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Endpoint público: não expõe err.Error() das deps (pode vazar host/DSN);
	// só OK true/false por dependência.
	checks := map[string]svcStatus{}
	dbErr := s.db.Ping(ctx)
	checks["db"] = svcStatus{OK: dbErr == nil}
	rdErr := s.rdb.Ping(ctx).Err()
	checks["redis"] = svcStatus{OK: rdErr == nil}

	ready := dbErr == nil && rdErr == nil
	code := http.StatusOK
	if !ready {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"ready": ready, "checks": checks})
}
