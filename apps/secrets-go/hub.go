package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Hub distribui eventos (status, log, hit, stats) pra todos os clientes SSE
// conectados. Também guarda o ranking de keywords em memória (keywordCounts)
// — mantido incrementalmente aqui, e empurrado via evento "stats" a cada hit
// NOVO indexado (crawler.go), pra dispensar de vez o padrão antigo de
// "invalida a query no front a cada hit → refetch REST" (isso virava
// milhares de chamadas em /keywords/stats num scan/revalidação grande —
// SSE é pra empurrar dado, não servir de gatilho de polling escondido).
// Revalidação não incrementa: reprocessa hit já existente (mesmo doc ID),
// então a contagem por keyword não muda.
type Hub struct {
	mu            sync.Mutex
	clients       map[chan string]struct{}
	keywordCounts map[string]int
}

func NewHub() *Hub {
	return &Hub{clients: make(map[chan string]struct{}), keywordCounts: make(map[string]int)}
}

// SeedKeywordCounts inicializa o ranking em memória (chamado no boot, com o
// agregado real do Elasticsearch) — sem isso o ranking começaria zerado a
// cada restart até o próximo hit novo.
func (h *Hub) SeedKeywordCounts(counts map[string]int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keywordCounts = make(map[string]int, len(counts))
	for k, v := range counts {
		h.keywordCounts[k] = v
	}
}

// ResetKeywordCounts zera o ranking (chamado por handleClear, junto da
// limpeza do índice — senão o ranking ficaria "fantasma" com números de
// hits que não existem mais).
func (h *Hub) ResetKeywordCounts() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keywordCounts = make(map[string]int)
}

// IncKeywordCount soma 1 pra keyword e devolve uma cópia do mapa completo
// (pra broadcast) — cópia porque o mapa interno segue protegido pelo mutex,
// não pode vazar referência direta pros consumidores do evento.
func (h *Hub) IncKeywordCount(keyword string) map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keywordCounts[keyword]++
	out := make(map[string]int, len(h.keywordCounts))
	for k, v := range h.keywordCounts {
		out[k] = v
	}
	return out
}

func (h *Hub) Broadcast(ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	msg := string(b)
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c <- msg:
		default:
			// cliente lento: descarta em vez de travar o broadcast
		}
	}
}

func (h *Hub) ServeSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming não suportado", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 32)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
	}()

	fmt.Fprint(w, "event: ping\ndata: {}\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}
