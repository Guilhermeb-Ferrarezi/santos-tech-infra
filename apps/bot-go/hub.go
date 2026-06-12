package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 60 * time.Second
	wsPingPeriod = (wsPongWait * 9) / 10
	wsMaxMsgSize = 4096
)

// WSEvent é o payload enviado a todos os clientes conectados.
type WSEvent struct {
	Type           string              `json:"type"`
	ConversationID string              `json:"conversationId,omitempty"`
	Log            *dashProcessingLog  `json:"log,omitempty"`
}

// wsClient representa uma conexão WebSocket aberta.
type wsClient struct {
	hub  *WSHub
	conn *websocket.Conn
	send chan []byte
}

// WSHub gerencia todas as conexões WebSocket do dashboard.
type WSHub struct {
	clients    map[*wsClient]struct{}
	broadcast  chan []byte
	register   chan *wsClient
	unregister chan *wsClient
	mu         sync.Mutex
	upgrader   websocket.Upgrader
	logger     *slog.Logger
}

// NewWSHub cria um hub com validação de origem.
// allowedOrigin vazio recusa todas as conexões (fail-closed).
func NewWSHub(logger *slog.Logger, allowedOrigin string) *WSHub {
	return &WSHub{
		clients:    make(map[*wsClient]struct{}),
		broadcast:  make(chan []byte, 128),
		register:   make(chan *wsClient, 16),
		unregister: make(chan *wsClient, 16),
		logger:     logger,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				if allowedOrigin == "" {
					return false
				}
				origin := r.Header.Get("Origin")
				return origin == allowedOrigin
			},
		},
	}
}

// Run inicia o loop do hub; bloqueante até ctx ser cancelado.
func (h *WSHub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for c := range h.clients {
				_ = c.conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"))
				close(c.send)
			}
			h.clients = make(map[*wsClient]struct{})
			h.mu.Unlock()
			return

		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()

		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()

		case msg := <-h.broadcast:
			h.mu.Lock()
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					// canal cheio — cliente lento, remove
					delete(h.clients, c)
					close(c.send)
				}
			}
			h.mu.Unlock()
		}
	}
}

// Broadcast serializa um WSEvent e envia a todos os clientes.
func (h *WSHub) Broadcast(ev WSEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		h.logger.Error("ws hub: marshal event", "err", err)
		return
	}
	select {
	case h.broadcast <- b:
	default:
		h.logger.Warn("ws hub: broadcast channel full, dropping event", "type", ev.Type)
	}
}

// Upgrade faz o handshake HTTP→WebSocket e registra o cliente no hub.
func (h *WSHub) Upgrade(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// o upgrader já escreveu um HTTP 400 se o origin não bate
		h.logger.Debug("ws hub: upgrade failed", "err", err)
		return
	}
	c := &wsClient{hub: h, conn: conn, send: make(chan []byte, 64)}
	h.register <- c
	go c.writePump()
	c.readPump() // bloqueia até o cliente fechar
}

// readPump drena mensagens do cliente (não usamos, mas precisamos para detectar fechamento e pongs).
func (c *wsClient) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(wsMaxMsgSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

// writePump envia mensagens do canal send e pings periódicos.
func (c *wsClient) writePump() {
	ticker := time.NewTicker(wsPingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
