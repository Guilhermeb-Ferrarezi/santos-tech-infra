package main

import (
	"encoding/json"
	"time"
)

// IDs de domínio — strings UUID tipadas por alias para evitar troca acidental.
type TenantID = string
type ContactID = string
type ChannelIdentityID = string
type ConversationID = string
type LeadID = string
type MessageID = string
type DomainEventID = string
type WebhookEventID = string
type ScheduledContactID = string

// ConversationState — FSM da conversa.
type ConversationState string

const (
	StateNew                   ConversationState = "NEW"
	StateEngaged               ConversationState = "ENGAGED"
	StateAwaitingReply         ConversationState = "AWAITING_REPLY"
	StateFollowupPending       ConversationState = "FOLLOWUP_PENDING"
	StateConcludedPositive     ConversationState = "CONCLUDED_POSITIVE"
	StateConcludedNegative     ConversationState = "CONCLUDED_NEGATIVE"
	StateConcludedNoAnswer     ConversationState = "CONCLUDED_NO_ANSWER"
	StateHandoff               ConversationState = "HANDOFF"
	StateDormant               ConversationState = "DORMANT"
	StateReactivationScheduled ConversationState = "REACTIVATION_SCHEDULED"
)

// CommunicationStyle — estilo detectado do cliente para espelhamento.
type CommunicationStyle string

const (
	StyleFormal    CommunicationStyle = "formal"
	StyleTechnical CommunicationStyle = "technical"
	StyleCasual    CommunicationStyle = "casual"
	StylePlain     CommunicationStyle = "plain"
)

// OutboundIntent — intenção da mensagem de saída.
type OutboundIntent string

const (
	IntentFreeForm               OutboundIntent = "FREE_FORM"
	IntentStructuredReengagement OutboundIntent = "STRUCTURED_REENGAGEMENT"
	IntentTransactionalUpdate    OutboundIntent = "TRANSACTIONAL_UPDATE"
)

// Contact — entidade raiz de identidade (desacoplada de canal).
type Contact struct {
	ID                 ContactID
	TenantID           TenantID
	DisplayName        string
	CommunicationStyle *CommunicationStyle
	LeadSummary        *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ChannelIdentity — telefone/id no canal (um contato pode ter vários).
type ChannelIdentity struct {
	ID         ChannelIdentityID
	TenantID   TenantID
	ContactID  ContactID
	Channel    string
	ExternalID string // número de telefone E.164
	CreatedAt  time.Time
}

// Conversation — thread de conversa por canal.
type Conversation struct {
	ID                  ConversationID
	TenantID            TenantID
	ContactID           ContactID
	ChannelIdentityID   ChannelIdentityID
	Channel             string
	State               ConversationState
	BotEnabled          bool
	LastInboundAt       *time.Time
	LastOutboundAt      *time.Time
	LastInboundModality string
	Summary             *string
	StructuredFacts     map[string]any
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Lead — oportunidade de negócio gerada pela conversa.
type Lead struct {
	ID             LeadID
	TenantID       TenantID
	ContactID      ContactID
	ConversationID ConversationID
	Status         string
	CreatedAt      time.Time
}

// TenantConfig — configurações do tenant (KB, persona, quiet hours, etc.).
type TenantConfig struct {
	TenantID             TenantID
	BotName              string
	BotGender            string
	RevealAIIfAsked      bool
	KBContent            *string
	SystemPrompt         string
	AdminSystemPrompt    string
	Timezone             string
	QuietHoursStart      *string
	QuietHoursEnd        *string
	BotEnabledByDefault  bool
	BotAllowedNumbers    []string
	AdminWhatsAppNumber  string   // legado (0014) — mantido por compatibilidade
	AdminWhatsAppNumbers []string // múltiplos admins (0016) — fonte de verdade

	// Transient — não vem do banco; setado pelo engine antes de chamar o Responder.
	IsAdminConversation bool
	// AllowedWebURLs — páginas (do sitemap) que o bot pode consultar via WebFetch.
	AllowedWebURLs []string
	// Schedule — aulas já agendadas (do Notion), injetadas no prompt do cliente
	// para o bot propor horários livres. Vazio = agendamento desabilitado/sem dados.
	Schedule []ScheduleEntry
}

// ScheduleEntry — uma aula experimental já agendada, lida do data source
// "Agenda — Aulas Experimentais" do Notion (horários ocupados).
type ScheduleEntry struct {
	Aluno     string // Aluno/Responsável (title)
	DataHora  string // ISO 8601 (start do campo "Data e hora")
	Display   string // formatado pra humano, ex.: "ter 17/06 às 19:30"
	Status    string // Agendada | Confirmar | Em andamento | Feita | Faltou | Remarcou
	Professor string // Professor(a) (person) — best-effort, pode vir vazio
	WhatsApp  string
}

// Booking — dados para gravar uma aula experimental na agenda do Notion.
type Booking struct {
	Aluno    string // Aluno/Responsável (title)
	WhatsApp string // telefone do cliente (E.164 ou como veio)
	DataHora string // ISO 8601 com hora; vazio = não resolvido (cai pra Status "Confirmar")
	Status   string // "Agendada" (com data) | "Confirmar" (sem data resolvida)
}

// SchedulingRequest — pedido de agendamento detectado pelo LLM na conversa do cliente.
type SchedulingRequest struct {
	Kind           string // "experimental" | "individual"
	StudentName    string
	Age            int
	Course         string
	ProposedDay    string
	ProposedTime   string
	ProposedPeriod string
	Notes          string
}

// BookingAction — ação do admin sobre um agendamento pendente.
type BookingAction struct {
	BookingID string
	Action    string // "confirm" | "adjust" | "reject"
	Day       string
	Time      string
	Period    string
}

// PendingBooking — agendamento aguardando confirmação do admin. Ver migration 0018.
type PendingBooking struct {
	ID             string
	TenantID       TenantID
	ConversationID ConversationID
	ClientPhone    string
	ClientName     string
	Kind           string
	Course         string
	ProposedDay    string
	ProposedTime   string
	ProposedPeriod string
	Age            int
	Notes          string
	Status         string // open | confirmed | rejected
	// Channel — canal de origem do CLIENTE (whatsapp | evolution). Define por qual
	// sender o aviso/resposta sai. Ver migration 0023.
	Channel   string
	CreatedAt time.Time
}

// IsAdminNumber retorna true se o telefone for de um administrador (lista 0016,
// com fallback ao número único legado).
func (c TenantConfig) IsAdminNumber(phone string) bool {
	if phone == "" {
		return false
	}
	for _, n := range c.AdminWhatsAppNumbers {
		if n == phone {
			return true
		}
	}
	return c.AdminWhatsAppNumber != "" && c.AdminWhatsAppNumber == phone
}

// AdminNumbers retorna a lista efetiva de números de admin (lista 0016 ou, se
// vazia, o número único legado).
func (c TenantConfig) AdminNumbers() []string {
	if len(c.AdminWhatsAppNumbers) > 0 {
		return c.AdminWhatsAppNumbers
	}
	if c.AdminWhatsAppNumber != "" {
		return []string{c.AdminWhatsAppNumber}
	}
	return nil
}

// KBEntry — entrada de base de conhecimento usada para extração e persistência.
type KBEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
}

// KnowledgeBaseEntry — entrada da base de conhecimento para o prompt.
type KnowledgeBaseEntry struct {
	ID      string
	Title   string
	Content string
}

// InboundMessage — mensagem recebida do WhatsApp (formato canônico).
type InboundMessage struct {
	TenantID          TenantID
	Channel           string
	ExternalID        string // telefone do remetente
	DisplayHandle     string
	ProviderMessageID string // wamid da Meta
	Content           MessageContent
	ReceivedAt        time.Time
}

// MessageContent — conteúdo de uma mensagem (texto, imagem, áudio...).
type MessageContent struct {
	Type       string  // "text" | "image" | "audio" | "video" | "document" | "sticker" | "location"
	Text       string  // preenchido quando Type=="text"
	Transcript *string // transcrição STT quando Type=="audio"
	Caption    *string
	MediaURL   *string
	MimeType   *string
}

// OutboundMessage — mensagem a enviar (formato canônico, exactly-once via chave).
type OutboundMessage struct {
	TenantID        TenantID
	ConversationID  ConversationID
	Channel         string
	To              string // telefone E.164 de destino
	Intent          OutboundIntent
	Content         MessageContent
	IdempotencyKey  string
	TemplatePayload *TemplatePayload
}

// TemplatePayload — template Meta para reengajamento fora da janela 24h.
type TemplatePayload struct {
	Name      string
	Language  string
	Variables map[string]string
}

// ConversationTurn — turno do histórico recente (cliente ou bot).
type ConversationTurn struct {
	Role string // "user" | "assistant"
	Text string
}

// ConversationContext — contexto montado para o prompt do LLM.
type ConversationContext struct {
	RecentTurns     []ConversationTurn
	Summary         string
	StructuredFacts map[string]any
	// PendingQuestions — dúvidas de clientes aguardando resposta. Injetadas
	// SOMENTE em conversas admin, para o LLM casar a info fornecida e rascunhar.
	PendingQuestions []PendingQuestion
	// PendingBookings — agendamentos aguardando confirmação. Injetados SOMENTE
	// em conversas admin, para o LLM confirmar/ajustar/rejeitar.
	PendingBookings []PendingBooking
}

// PendingQuestion — dúvida de um cliente que o bot não soube responder (handoff),
// aguardando o admin fornecer a informação. Ver migration 0015.
type PendingQuestion struct {
	ID             string
	TenantID       TenantID
	ConversationID ConversationID
	ClientPhone    string
	ClientName     string
	Question       string
	Status         string // open | drafted | resolved
	Draft          string
	// Channel — canal de origem do CLIENTE (whatsapp | evolution). Define por qual
	// sender a resposta sai. Ver migration 0023.
	Channel   string
	CreatedAt time.Time
}

// ClientAction — ação proposta pelo LLM (modo admin) sobre uma dúvida pendente:
// rascunhar (Send=false) ou enviar de fato ao cliente (Send=true).
type ClientAction struct {
	PendingID string
	Draft     string
	Send      bool
}

// ToolCall — uso de ferramenta capturado durante geração do LLM.
type ToolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// ResponderOutput — saída parseada do LLM (contrato JSON do prompt).
type ResponderOutput struct {
	Bubbles           []string
	Answered          bool
	AnsweredFromKb    bool
	CitedEntryIDs     []string
	Handoff           bool
	Smalltalk         bool // saudação/agradecimento/conversa fiada — não conta como gap de KB
	ScheduledContact  *ScheduledContact
	QuotedReplies     []QuotedReply
	ToolCalls         []ToolCall
	KBEntry           *KBEntry           // presente quando admin fornece info para salvar na KB
	ClientActions     []ClientAction     // modo admin: rascunhos/envios para clientes pendentes
	SchedulingRequest *SchedulingRequest // cliente: pedido de agendamento detectado
	BookingActions    []BookingAction    // modo admin: confirmar/ajustar/rejeitar agendamentos
}

// ScheduledContact — reativação pedida pelo cliente ("me chama em julho").
type ScheduledContact struct {
	RawPhrase    string
	ResolvedDate string // YYYY-MM-DD
	Confidence   float64
}

// QuotedReply — citação de mensagem anterior em um balão de resposta.
type QuotedReply struct {
	Bubble int    // índice em Bubbles
	Ref    string // marcador [mN]
}

// DomainEvent — evento emitido pelo engine e gravado no outbox.
type DomainEvent struct {
	ID          DomainEventID
	TenantID    TenantID
	AggregateID string
	Type        string
	Payload     map[string]any
	OccurredAt  time.Time
}

// WebhookEvent — log de webhooks recebidos (para dedup e retry).
type WebhookEvent struct {
	ID              WebhookEventID
	TenantID        TenantID
	Provider        string
	ProviderEventID string
	RawPayload      []byte
	Status          string // "pending" | "done" | "failed"
	Attempts        int
	NextRetryAt     *time.Time
	LastError       *string
	ProcessedAt     *time.Time
	CreatedAt       time.Time
}

// ScheduledContactRow — follow-up ou reativação agendada.
type ScheduledContactRow struct {
	ID             ScheduledContactID
	TenantID       TenantID
	ContactID      ContactID
	ConversationID ConversationID
	Kind           string // "follow_up" | "reactivation"
	ScheduledAt    time.Time
	Status         string // "pending" | "sent" | "skipped" | "failed"
	Attempts       int
	TemplateVars   map[string]string
	CreatedAt      time.Time
}

// Persona — identidade do bot configurada pelo tenant.
type Persona struct {
	BotName         string
	BotGender       string
	RevealAIIfAsked bool
}
