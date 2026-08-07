package main

import "strings"

// UserAgentInfo é o resultado da heurística de parsing do User-Agent — nunca
// confiar no que o cliente declara, sempre derivar isto no servidor a partir do
// header real da requisição (mesmo motivo anti-forja do resto do domínio de
// analytics: o cliente não pode inflar/forjar a métrica).
type UserAgentInfo struct {
	Device  string // "mobile" | "tablet" | "desktop"
	Browser string // "Chrome" | "Safari" | "Firefox" | "Edge" | ""
	OS      string // "iOS" | "Android" | "Windows" | "macOS" | "Linux" | ""
}

// parseUserAgent é uma heurística simples baseada em substring — não é uma lib
// completa de UA sniffing, é o suficiente pra dashboard de métricas (mesmo nível
// de esforço que o loja-3d usa no próprio analytics).
func parseUserAgent(ua string) UserAgentInfo {
	var info UserAgentInfo

	switch {
	case strings.Contains(ua, "iPad"):
		info.Device = "tablet"
	case strings.Contains(ua, "Tablet") || strings.Contains(ua, "SM-T"):
		info.Device = "tablet"
	case strings.Contains(ua, "Mobi") || strings.Contains(ua, "iPhone") || strings.Contains(ua, "Android"):
		info.Device = "mobile"
	default:
		info.Device = "desktop"
	}
	// "Android" sozinho (sem "Mobi") em geral ainda é celular no nosso público;
	// só reclassifica como tablet se vier explicitamente marcado acima.
	if info.Device == "desktop" && strings.Contains(ua, "Android") {
		info.Device = "mobile"
	}

	switch {
	case strings.Contains(ua, "Edg/"):
		info.Browser = "Edge"
	case strings.Contains(ua, "Chrome/"):
		info.Browser = "Chrome"
	case strings.Contains(ua, "Firefox/"):
		info.Browser = "Firefox"
	case strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/"):
		info.Browser = "Safari"
	}

	switch {
	case strings.Contains(ua, "iPhone OS") || strings.Contains(ua, "CPU OS"):
		info.OS = "iOS"
	case strings.Contains(ua, "Android"):
		info.OS = "Android"
	case strings.Contains(ua, "Windows"):
		info.OS = "Windows"
	case strings.Contains(ua, "Mac OS X"):
		info.OS = "macOS"
	case strings.Contains(ua, "Linux"):
		info.OS = "Linux"
	}

	return info
}

// referrerDomain reduz uma URL de referrer completa ao domínio (ex.:
// "https://www.google.com/search?q=x" → "google.com") — evita que a mesma
// origem apareça picada em dezenas de linhas diferentes no ranking de
// referrers por causa de query string/path. "www." é removido para não
// duplicar linha com/sem www. String vazia ou não-parseável vira "" (direto).
func referrerDomain(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	s := raw
	if i := strings.Index(s, "://"); i != -1 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i != -1 {
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i != -1 { // user:pass@host raro, mas não deixa vazar
		s = s[i+1:]
	}
	s = strings.TrimPrefix(s, "www.")
	return strings.ToLower(s)
}
