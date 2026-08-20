package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// sseKeepAliveInterval é o intervalo do ping dos streams SSE. Precisa ser menor que
// qualquer idle timeout de proxy no caminho (Cloudflare/Traefik).
const sseKeepAliveInterval = 20 * time.Second

// startSSE prepara a resposta para Server-Sent Events e devolve o flusher.
//
// O WriteTimeout do http.Server (60s) é o prazo da resposta INTEIRA, não de cada
// escrita: sem removê-lo aqui, todo stream SSE morria aos 60 segundos e o cliente
// NUNCA recebia o evento 'paid' de um PIX pago depois disso — a tela ficava girando
// com o dinheiro já na conta. SetWriteDeadline(zero) desliga o prazo só nesta conexão.
func startSSE(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		// Sem controle de deadline (ex.: middleware que embrulha o ResponseWriter), o
		// stream ainda funciona — só volta a morrer no WriteTimeout. Precisa aparecer.
		slog.Warn("SSE: não foi possível remover o write deadline; o stream vai expirar no WriteTimeout", "err", err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Desliga o buffering de proxies que ignoram o Content-Type de stream.
	w.Header().Set("X-Accel-Buffering", "no")
	return flusher, true
}

// sseKeepAlive escreve um comentário SSE (linha iniciada por ':'), ignorado pelo
// EventSource do navegador — serve só para manter a conexão viva pelos proxies.
func sseKeepAlive(w http.ResponseWriter, flusher http.Flusher) {
	fmt.Fprint(w, ": keepalive\n\n")
	flusher.Flush()
}
