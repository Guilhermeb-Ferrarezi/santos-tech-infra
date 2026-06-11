package main

import "time"

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
	StateNew                    ConversationState = "NEW"
	StateEngaged                ConversationState = "ENGAGED"
	StateAwaitingReply          ConversationState = "AWAITING_REPLY"
	StateFollowupPending        ConversationState = "FOLLOWUP_PENDING"
	StateConcludedPositive      ConversationState = "CONCLUDED_POSITIVE"
	StateConcludedNegative      ConversationState = "CONCLUDED_NEGATIVE"
	StateConcludedNoAnswer      ConversationState = "CONCLUDED_NO_ANSWER"
	StateHandoff                ConversationState = "HANDOFF"
	StateDormant                ConversationState = "DORMANT"
	StateReactivationScheduled  ConversationState = "REACTIVATION_SCHEDULED"
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
	IntentFreeForm              OutboundIntent = "FREE_FORM"
	IntentStructuredReengagement OutboundIntent = "STRUCTURED_REENGAGEMENT"
	IntentTransactionalUpdate   OutboundIntent = "TRANSACTIONAL_UPDATE"
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
	ID                      ConversationID
	TenantID                TenantID
	ContactID               ContactID
	ChannelIdentityID       ChannelIdentityID
	Channel                 string
	State                   ConversationState
	BotEnabled              bool
	LastInboundAt           *time.Time
	LastOutboundAt          *time.Time
	LastInboundModality     string
	Summary                 *string
	StructuredFacts         map[string]any
	CreatedAt               time.Time
	UpdatedAt               time.Time
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
	Timezone             string
	QuietHoursStart      *string
	QuietHoursEnd        *string
	BotEnabledByDefault  bool
	BotAllowedNumbers    []string
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
	TenantID          TenantID
	ConversationID    ConversationID
	Channel           string
	To                string // telefone E.164 de destino
	Intent            OutboundIntent
	Content           MessageContent
	IdempotencyKey    string
	TemplatePayload   *TemplatePayload
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
	RecentTurns    []ConversationTurn
	Summary        string
	StructuredFacts map[string]any
}

// ResponderOutput — saída parseada do LLM (contrato JSON do prompt).
type ResponderOutput struct {
	Bubbles          []string
	Answered         bool
	AnsweredFromKb   bool
	CitedEntryIDs    []string
	Handoff          bool
	ScheduledContact *ScheduledContact
	QuotedReplies    []QuotedReply
}

// ScheduledContact — reativação pedida pelo cliente ("me chama em julho").
type ScheduledContact struct {
	RawPhrase    string
	ResolvedDate string  // YYYY-MM-DD
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
	BotName        string
	BotGender      string
	RevealAIIfAsked bool
}
