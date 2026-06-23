package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/santos-tech/cron-go/db"
)

type dispatchResult struct {
	HTTPStatus int
	Excerpt    string
	Err        error
}

// hostAllowed: bloqueia localhost/IP privado/link-local/unspecified de forma
// INCONDICIONAL — a allowlist nunca pode reabilitar esses endereços (guard anti-SSRF).
// Só depois dos bloqueios é que match exato ou sufixo da allowlist decide o true.
func hostAllowed(host string, allowlist []string) bool {
	h := host
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i] // tira porta
	}
	if h == "" {
		return false
	}

	// Bloqueios INCONDICIONAIS (anti-SSRF): nenhuma entrada da allowlist pode
	// sobrepor estes. Verificados antes de qualquer match de allowlist.
	if h == "localhost" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return false
		}
	}

	// Match exato (host com porta ou sem).
	for _, suf := range allowlist {
		if suf == "" {
			continue
		}
		if host == suf || h == suf {
			return true
		}
	}

	// Match por sufixo (ex.: ".santos-tech.com" cobre subdomínios).
	for _, suf := range allowlist {
		if suf == "" {
			continue
		}
		if h == strings.TrimPrefix(suf, ".") || strings.HasSuffix(h, suf) {
			return true
		}
	}
	return false
}

func (s *Server) buildTargetURL(job db.CronJob) (method, rawURL string, err error) {
	switch job.ActionKind {
	case "catalog":
		a, ok := lookupCatalog(job.ActionRef)
		if !ok {
			return "", "", fmt.Errorf("ação de catálogo desconhecida: %s", job.ActionRef)
		}
		scheme := a.Scheme
		if scheme == "" {
			scheme = "https"
		}
		return a.Method, scheme + "://" + a.Host + a.Path, nil
	case "http":
		if !s.cfg.AllowRawHTTP {
			return "", "", fmt.Errorf("HTTP cru desabilitado (CRON_ALLOW_RAW_HTTP)")
		}
		m := job.HttpMethod
		if m == "" {
			m = http.MethodPost
		}
		return m, job.HttpUrl, nil
	default:
		return "", "", fmt.Errorf("action_kind inválido: %s", job.ActionKind)
	}
}

func (s *Server) dispatch(ctx context.Context, job db.CronJob) dispatchResult {
	method, rawURL, err := s.buildTargetURL(job)
	if err != nil {
		return dispatchResult{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return dispatchResult{Err: err}
	}
	if !s.hostCheck(req.URL.Host) {
		return dispatchResult{Err: fmt.Errorf("host fora da allowlist: %s", req.URL.Host)}
	}
	if s.cfg.ServicePAT != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.ServicePAT)
	}
	req.Header.Set("Content-Type", "application/json")

	timeout := time.Duration(job.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("muitos redirects (>= 5)")
			}
			if !s.hostCheck(req.URL.Host) {
				return fmt.Errorf("redirect bloqueado: host fora da allowlist: %s", req.URL.Host)
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return dispatchResult{Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	res := dispatchResult{HTTPStatus: resp.StatusCode, Excerpt: string(body)}
	if resp.StatusCode >= 400 {
		res.Err = fmt.Errorf("alvo respondeu %d", resp.StatusCode)
	}
	return res
}
