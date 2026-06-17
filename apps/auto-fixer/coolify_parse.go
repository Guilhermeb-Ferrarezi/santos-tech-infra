package main

import "strings"

// coolifyDeployStatus classifica o evento da Coolify num status normalizado.
// Retorna "failed" | "success" | "unhealthy" | "started" | "" (desconhecido).
// É defensiva com os vários formatos: campos de topo `status`/`type`/`message`,
// e os sub-objetos `deployment`/`data`.
func coolifyDeployStatus(p map[string]any) string {
	var signals []string
	add := func(v any) {
		if s, ok := v.(string); ok && s != "" {
			signals = append(signals, strings.ToLower(s))
		}
	}
	add(p["status"])
	add(p["type"])
	add(p["event"])
	add(p["state"])
	add(p["message"])
	if dep, ok := p["deployment"].(map[string]any); ok {
		add(dep["status"])
		add(dep["state"])
	}
	if data, ok := p["data"].(map[string]any); ok {
		add(data["status"])
		add(data["state"])
		add(data["type"])
	}

	// Ordem importa: falha tem prioridade sobre o resto.
	for _, sig := range signals {
		if strings.Contains(sig, "fail") || strings.Contains(sig, "error") {
			return "failed"
		}
	}
	for _, sig := range signals {
		if strings.Contains(sig, "unhealthy") || strings.Contains(sig, "stopped") ||
			strings.Contains(sig, "exited") || strings.Contains(sig, "down") ||
			strings.Contains(sig, "crashed") {
			return "unhealthy"
		}
	}
	for _, sig := range signals {
		if strings.Contains(sig, "success") || strings.Contains(sig, "finished") ||
			strings.Contains(sig, "deployed") || strings.Contains(sig, "healthy") ||
			strings.Contains(sig, "running") {
			return "success"
		}
	}
	for _, sig := range signals {
		if strings.Contains(sig, "start") || strings.Contains(sig, "queued") ||
			strings.Contains(sig, "progress") || strings.Contains(sig, "building") {
			return "started"
		}
	}
	return ""
}

// coolifyAppName extrai o nome da aplicação de forma defensiva.
func coolifyAppName(p map[string]any) string {
	for _, k := range []string{"application_name", "name", "service_name", "project_name"} {
		if v, ok := p[k].(string); ok && v != "" {
			return v
		}
	}
	if dep, ok := p["deployment"].(map[string]any); ok {
		for _, k := range []string{"application_name", "name"} {
			if v, ok := dep[k].(string); ok && v != "" {
				return v
			}
		}
	}
	if app, ok := p["application"].(map[string]any); ok {
		if v, ok := app["name"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// coolifyString busca a primeira chave não-vazia (string) no payload e, opcionalmente,
// no sub-objeto `deployment`.
func coolifyString(p map[string]any, keys ...string) string {
	lookup := func(m map[string]any) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	if v := lookup(p); v != "" {
		return v
	}
	if dep, ok := p["deployment"].(map[string]any); ok {
		if v := lookup(dep); v != "" {
			return v
		}
	}
	return ""
}

// shortSHA encurta um hash de commit para 7 caracteres (se parecer um SHA).
func shortSHA(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
