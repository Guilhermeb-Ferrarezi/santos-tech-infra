package main

import "time"

// Papéis no ecossistema: 1=Student, 2=Teacher, 3=Admin, 4=Custom.
// Este serviço é privilegiado por design (roda Claude com --dangerously-skip-permissions),
// então só Admin tem acesso.
const RoleAdmin = 3

// Status de uma conversa / turno.
const (
	StatusIdle    = "idle"
	StatusRunning = "running"
	StatusError   = "error"
)

// Conversation = uma sessão Claude isolada e retomável.
// ID é o nosso PK estável (URLs, FK das mensagens). SessionID é o --session-id do
// claude CLI, que pode ser rotacionado (/clear, /compact) sem perder o histórico.
type Conversation struct {
	ID             string    `json:"id"`
	UserID         int64     `json:"userId"`
	Title          *string   `json:"title"`
	Repo           *string   `json:"repo"`
	Workdir        string    `json:"workdir"`
	Model          string    `json:"model"`
	Status         string    `json:"status"`
	SessionID      string    `json:"sessionId"`
	SessionStarted bool      `json:"-"` // true após o 1º turno da sessão atual
	ToolsDisabled  bool      `json:"toolsDisabled"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Message = um evento (normalizado) do stream-json do Claude, persistido.
type Message struct {
	ID             int64          `json:"id"`
	ConversationID string         `json:"conversationId"`
	Role           string         `json:"role"` // user|assistant|system|tool
	Kind           string         `json:"kind"` // text|tool_use|tool_result|result
	Content        map[string]any `json:"content"`
	Usage          map[string]any `json:"usage,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
}
