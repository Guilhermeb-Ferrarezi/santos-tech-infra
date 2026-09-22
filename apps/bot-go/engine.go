package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrMessageHeld sinaliza que a mensagem NÃO foi processada agora — foi RETIDA
// para reprocessamento futuro (ex.: quiet hours). Quem chamou (flushBurst) NÃO
// deve marcar o webhook como 'done' nesse caso: o SetNextRetryAt já o deixou em
// 'failed' + next_retry_at, e o drenador de retries do worker o reprocessará.
var ErrMessageHeld = errors.New("mensagem retida para reprocessamento")

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

// Responder invoca o LLM para produzir balões de resposta.
type Responder interface {
	Respond(ctx context.Context, conv Conversation, convCtx ConversationContext, cfg TenantConfig, inboundText string) (ResponderOutput, error)
}

// ChatSender envia mensagens de saída pelo canal (WhatsApp, etc.).
type ChatSender interface {
	SendMessage(ctx context.Context, msg OutboundMessage) (providerMessageID string, err error)
	SendText(ctx context.Context, to, text string) error
	// SendTypingIndicator exibe "digitando…" para a mensagem inbound informada.
	SendTypingIndicator(ctx context.Context, messageID string) error
}

// EventEmitter grava domain events no outbox (dentro da transação corrente).
type EventEmitter interface {
	Emit(ctx context.Context, tx pgx.Tx, event DomainEvent) error
}

// ---------------------------------------------------------------------------
// EngineDeps
// ---------------------------------------------------------------------------

// EngineDeps agrupa todas as dependências injetadas no ConversationEngine.
type EngineDeps struct {
	TenantID  TenantID
	DB        *pgxpool.Pool
	Contacts  *ContactRepo
	Convs     *ConversationRepo
	Messages  *MessageRepo
	Leads     *LeadRepo
	Config    *TenantConfigRepo
	Responder Responder
	Sender    ChatSender
	// EvolutionSender — sender do canal não-oficial (Evolution). Quando presente,
	// o engine roteia a resposta ao CLIENTE pelo sender correto conforme o canal de
	// origem do cliente (Meta para 'whatsapp', Evolution para 'evolution'), em vez de
	// usar sempre e.deps.Sender (que é o sender do canal do ADMIN/da conversa atual).
	EvolutionSender ChatSender
	Emitter         EventEmitter
	Logger          *slog.Logger
	// Broadcast envia um evento WebSocket a todos os clientes do dashboard (opcional).
	Broadcast func(ev WSEvent)
	// LogRepo persiste logs de processamento para o painel de logs (opcional).
	LogRepo *ProcessingLogRepo
	// Sleep é injetável para testes (padrão: time.Sleep).
	Sleep func(time.Duration)
	// TenantCfgRepo permite ao engine persistir entradas de KB (opcional).
	TenantCfgRepo *TenantConfigRepo
	// Pending — fila de dúvidas de clientes aguardando o admin (ciclo admin→cliente).
	Pending *PendingQuestionRepo
	// Bookings — agendamentos aguardando confirmação do admin.
	Bookings *PendingBookingRepo
	// Notion — cliente para ler/gravar a agenda de aulas (agendamento).
	Notion *NotionClient
	// Voice — STT/TTS (opcional). Habilitado → responde em áudio às mensagens de voz.
	Voice *VoiceClient
	// AudioClips — banco de áudios pré-gravados (opcional). Usado quando o tenant
	// escolhe voice_provider='clips': em vez de sintetizar, envia a gravação real
	// do atendente. Sem clipe para o momento, registra a lacuna e cai no texto.
	AudioClips *AudioClipStore
	// AudioMatchMin — piso de semelhança entre a resposta escrita e a fala
	// gravada (0 = default do casamento). Subir recusa mais; descer arrisca
	// mandar áudio que não diz exatamente o que foi respondido.
	AudioMatchMin float64
	// AudioMatchMaxMs — teto de duração da gravação enviada. Um monólogo de 40s
	// em cima de uma dúvida de dez palavras é constrangedor mesmo quando casa.
	AudioMatchMaxMs int
	// AudioMatchShadow — decide e registra no log, mas responde em texto. Serve
	// para medir a taxa de acerto em produção antes de deixar o áudio sair.
	AudioMatchShadow bool
	// AgendaAutoConfirm — o bot grava a aula no Notion sem esperar um humano.
	// Desligado por padrão: ligar só depois das travas verificadas em produção.
	AgendaAutoConfirm bool
	// Funcionamento e duração da aula. Vêm do ambiente e são copiados para o
	// TenantConfig a cada mensagem — tenant_config não tem colunas para eles.
	EscolaAbre     string
	EscolaFecha    string
	AulaDuracaoMin int
	// GCal / GCalRepo — Google Agenda. Nil quando não configurado; o
	// agendamento continua funcionando sem eles.
	GCal     *GCalClient
	GCalRepo *GCalRepo
	// Lembretes — os três avisos ao cliente antes da aula.
	Lembretes *LembreteRepo
	// ForceBotEnabled — força o bot ativo nas conversas deste engine (ex.: canal
	// Evolution, cujo gate é o toggle externo, não o whitelist do tenant).
	ForceBotEnabled bool
}

// ---------------------------------------------------------------------------
// ConversationEngine
// ---------------------------------------------------------------------------

// ConversationEngine processa mensagens inbound e orquestra o FSM de conversa.
type ConversationEngine struct {
	deps       EngineDeps
	withTenant func(ctx context.Context, fn func(pgx.Tx) error) error
}

// NewConversationEngine cria um engine com as dependências fornecidas.
func NewConversationEngine(deps EngineDeps) *ConversationEngine {
	sleep := deps.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	deps.Sleep = sleep

	return &ConversationEngine{
		deps:       deps,
		withTenant: withTenant(deps.DB, deps.TenantID),
	}
}

// senderFor escolhe o ChatSender adequado ao canal informado. 'evolution' usa o
// EvolutionSender (número não-oficial) quando disponível; qualquer outro canal
// (ou ausência do sender Evolution) cai no Sender padrão (Meta/oficial). Isso
// garante que respostas a um cliente saiam pelo MESMO canal por onde ele falou,
// independentemente do canal da conversa em que o admin/engine está rodando.
func (e *ConversationEngine) senderFor(channel string) ChatSender {
	if channel == "evolution" && e.deps.EvolutionSender != nil {
		return e.deps.EvolutionSender
	}
	return e.deps.Sender
}

// ---------------------------------------------------------------------------
// Handle — ponto de entrada principal
// ---------------------------------------------------------------------------

// Handle processa uma mensagem inbound de ponta a ponta.
// O fluxo completo ocorre dentro de uma única transação por conta do withTenant,
// exceto pelos Sleeps de humanização que acontecem FORA da transação
// (o commit de cada balão anterior já foi realizado antes do Sleep seguinte).
//
// Nota de implementação: os balões são enviados e persistidos sequencialmente,
// cada um dentro de um withTenant separado para que o Sleep ocorra entre eles
// sem manter a transação aberta. O grosso do fluxo (resolução de contato,
// conversa, deduplificação, resposta do LLM) acontece em uma única transação
// inicial; os balões são gravados em transações individuais posteriores.
func (e *ConversationEngine) Handle(ctx context.Context, inbound InboundMessage) error {
	log := e.deps.Logger.With(
		"tenant_id", inbound.TenantID,
		"wamid", inbound.ProviderMessageID,
		"from", inbound.ExternalID,
	)

	// -----------------------------------------------------------------------
	// Fase 1: dentro de uma única transação — resolução, dedup, LLM
	// -----------------------------------------------------------------------

	handleStart := time.Now()

	var (
		conv          Conversation
		cfg           TenantConfig
		output        ResponderOutput
		inboundText   string
		mediaFallback bool
		contactPhone  = inbound.ExternalID
		contactName   string
		convCtx       ConversationContext
		llmReady      bool
		held          bool // mensagem retida (quiet hours) — não marcar webhook done
	)

	err := e.withTenant(ctx, func(tx pgx.Tx) error {
		// a) Configuração do tenant (necessária antes de criar a conversa)
		tenantCfg, err := e.deps.Config.Get(ctx, tx, inbound.TenantID)
		if err != nil {
			return fmt.Errorf("TenantConfig.Get: %w", err)
		}
		cfg = *tenantCfg
		// Funcionamento e duração da aula vêm do AMBIENTE, não da linha do
		// tenant — não existe coluna para eles em tenant_config.
		//
		// Sem esta cópia, o TenantConfig carregado do banco chega com os campos
		// vazios e o agendamento automático aborta em TODA tentativa com
		// "ESCOLA_ABRE inválido". Foi exatamente o que aconteceu em produção:
		// a variável estava configurada na Coolify e nunca chegava aqui.
		cfg.EscolaAbre = e.deps.EscolaAbre
		cfg.EscolaFecha = e.deps.EscolaFecha
		cfg.AulaDuracaoMin = e.deps.AulaDuracaoMin
		cfg.AgendaAutoConfirm = e.deps.AgendaAutoConfirm

		// b) Resolve contact + channel identity
		contact, chIdentity, err := e.deps.Contacts.FindByChannelIdentity(ctx, tx, inbound.Channel, inbound.ExternalID)
		if err != nil {
			return fmt.Errorf("FindByChannelIdentity: %w", err)
		}
		if contact == nil {
			contact, chIdentity, err = e.deps.Contacts.CreateWithChannelIdentity(ctx, tx, inbound.TenantID, inbound.Channel, inbound.ExternalID, inbound.DisplayHandle)
			if err != nil {
				return fmt.Errorf("CreateWithChannelIdentity: %w", err)
			}
		}
		contactName = contact.DisplayName

		// c) Resolve conversa
		convPtr, err := e.deps.Convs.FindByChannelIdentity(ctx, tx, chIdentity.ID)
		if err != nil {
			return fmt.Errorf("FindByChannelIdentity (conv): %w", err)
		}
		if convPtr == nil {
			convPtr, err = e.deps.Convs.Create(ctx, tx, inbound.TenantID, contact.ID, chIdentity.ID, inbound.Channel, isBotEnabledFor(cfg, inbound.ExternalID) || e.deps.ForceBotEnabled)
			if err != nil {
				return fmt.Errorf("Create conversation: %w", err)
			}
		}
		conv = *convPtr

		// c2) Captura de lead (CRM): todo cliente (não-admin) que escreve vira lead.
		// Idempotente — não rebaixa um lead que já avançou no funil.
		if e.deps.Leads != nil && !cfg.IsAdminNumber(contactPhone) {
			if _, err := e.deps.Leads.Create(ctx, tx, inbound.TenantID, contact.ID, conv.ID); err != nil {
				return fmt.Errorf("Leads.Create: %w", err)
			}
		}

		// d) Humano no controle → ignora (a menos que o engine force o bot, ex.: Evolution)
		if !conv.BotEnabled && !e.deps.ForceBotEnabled {
			log.Info("bot desabilitado para esta conversa, ignorando mensagem")
			return nil
		}

		// d) Deduplicação da mensagem inbound
		firstTime, err := e.deps.Messages.RecordInbound(ctx, tx, inbound.ProviderMessageID, inbound.TenantID, conv.ID, inbound.Content)
		if err != nil {
			return fmt.Errorf("RecordInbound: %w", err)
		}
		if !firstTime {
			log.Info("mensagem duplicada, ignorando", "wamid", inbound.ProviderMessageID)
			return nil
		}

		// e) Resolve inboundText (texto ou transcrição de áudio)
		switch inbound.Content.Type {
		case "text":
			inboundText = inbound.Content.Text
		case "audio":
			if inbound.Content.Transcript != nil {
				inboundText = *inbound.Content.Transcript
			}
		default:
			if inbound.Content.Caption != nil {
				inboundText = *inbound.Content.Caption
			}
		}

		// f) Mídia sem transcrição → resposta de fallback
		if inboundText == "" {
			mediaFallback = true
			return nil
		}

		// g) Emite domain_event "message.received"
		msgReceivedEvent := DomainEvent{
			TenantID:    inbound.TenantID,
			AggregateID: conv.ID,
			Type:        "message.received",
			Payload: map[string]any{
				"conversation_id": conv.ID,
				"contact_id":      contact.ID,
				"wamid":           inbound.ProviderMessageID,
				"modality":        inbound.Content.Type,
			},
			OccurredAt: inbound.ReceivedAt,
		}
		if err := e.deps.Emitter.Emit(ctx, tx, msgReceivedEvent); err != nil {
			return fmt.Errorf("Emit message.received: %w", err)
		}

		// h) Busca turnos recentes
		recentTurns, err := e.deps.Messages.GetRecentTurns(ctx, tx, conv.ID)
		if err != nil {
			return fmt.Errorf("GetRecentTurns: %w", err)
		}

		// i) Monta ConversationContext
		summary := ""
		if conv.Summary != nil {
			summary = *conv.Summary
		}
		convCtx = ConversationContext{
			RecentTurns:     recentTurns,
			Summary:         summary,
			StructuredFacts: conv.StructuredFacts,
		}

		// k) Quiet hours — verifica se deve suspender o processamento
		if cfg.QuietHoursStart != nil && cfg.QuietHoursEnd != nil {
			hold := QuietHoursHoldMs(inbound.ReceivedAt, cfg.Timezone, *cfg.QuietHoursStart, *cfg.QuietHoursEnd)
			if hold > 0 {
				log.Info("mensagem recebida em quiet hours, agendando retry", "hold", hold)
				nextRetry := inbound.ReceivedAt.Add(hold)
				if err := e.deps.Messages.SetNextRetryAt(ctx, tx, inbound.ProviderMessageID, nextRetry); err != nil {
					return fmt.Errorf("SetNextRetryAt: %w", err)
				}
				held = true
				return nil
			}
		}

		// l) Detecta conversa admin e sinaliza no cfg (transient). Em modo admin,
		// injeta as dúvidas pendentes para o LLM casar a info e rascunhar respostas.
		if cfg.IsAdminNumber(contactPhone) {
			cfg.IsAdminConversation = true
			if e.deps.Pending != nil {
				pendings, err := e.deps.Pending.ListOpen(ctx, tx, inbound.TenantID)
				if err != nil {
					return fmt.Errorf("Pending.ListOpen: %w", err)
				}
				convCtx.PendingQuestions = pendings
			}
			if e.deps.Bookings != nil {
				bks, err := e.deps.Bookings.ListOpen(ctx, tx, inbound.TenantID)
				if err != nil {
					return fmt.Errorf("Bookings.ListOpen: %w", err)
				}
				convCtx.PendingBookings = bks
			}
		}

		// Sinaliza para chamar o LLM fora da transação.
		llmReady = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("engine.Handle (fase 1): %w", err)
	}

	// Mensagem retida (quiet hours): a inbound já foi marcada para retry; sinaliza
	// ao chamador para NÃO marcar o webhook como done. O drenador de retries do
	// worker reprocessará quando next_retry_at vencer.
	if held {
		return ErrMessageHeld
	}

	// Broadcast da mensagem inbound logo após o commit — antes do LLM.
	// O dashboard recebe a mensagem imediatamente sem esperar a resposta.
	if err == nil && inboundText != "" && e.deps.Broadcast != nil {
		e.deps.Broadcast(WSEvent{Type: "message.inbound", ConversationID: conv.ID})
	}

	// Chama o LLM fora da transação (assíncrono ao commit da inbound).
	if llmReady {
		if e.deps.Broadcast != nil {
			e.deps.Broadcast(WSEvent{Type: "conversation.processing", ConversationID: conv.ID})
		}
		// Mostra "digitando…" no WhatsApp enquanto o LLM pensa.
		stopTyping := e.startTypingIndicator(ctx, inbound.ProviderMessageID)
		respOut, llmErr := e.deps.Responder.Respond(ctx, conv, convCtx, cfg, inboundText)
		stopTyping()
		if llmErr != nil {
			return fmt.Errorf("engine.Handle (responder): %w", llmErr)
		}
		output = respOut
	}

	// Persiste entrada de KB quando admin forneceu informação (após commit).
	// SÓ em conversa admin: o parser extrai kbEntry de qualquer resposta do
	// modelo, então sem esta guarda um cliente injeta o campo na mensagem e
	// escreve direto na base de conhecimento.
	if cfg.IsAdminConversation && output.KBEntry != nil && output.KBEntry.Content != "" && e.deps.TenantCfgRepo != nil {
		output.KBEntry.ID = fmt.Sprintf("admin-%d", time.Now().UnixMilli())
		if kbErr := e.deps.TenantCfgRepo.AppendKBEntry(ctx, inbound.TenantID, *output.KBEntry); kbErr != nil {
			log.Warn("engine: falha ao persistir kbEntry do admin", "err", kbErr)
		} else {
			log.Info("engine: entrada KB salva via admin", "title", output.KBEntry.Title)
		}
	}

	// Se não há output (bot desabilitado, dedup, quiet hours, mídia sem texto),
	// trata o caso de fallback de mídia fora da transação e encerra.
	if mediaFallback {
		if err := e.deps.Sender.SendText(ctx, inbound.ExternalID, "recebi sua mídia, em breve respondo"); err != nil {
			log.Error("erro ao enviar fallback de mídia", "err", err)
		}
		return nil
	}

	if len(output.Bubbles) == 0 {
		return nil
	}

	// -----------------------------------------------------------------------
	// Fase 2: envio dos balões (Sleep FORA da transação)
	// -----------------------------------------------------------------------

	wamid := inbound.ProviderMessageID

	// Serializa o output do LLM para armazenar como reasoning no primeiro balão.
	var reasoningJSON *string
	if rb, err := json.Marshal(map[string]any{
		"answered":       output.Answered,
		"answeredFromKb": output.AnsweredFromKb,
		"citedEntryIds":  output.CitedEntryIDs,
		"handoff":        output.Handoff,
	}); err == nil {
		s := string(rb)
		reasoningJSON = &s
	}

	// Modo espelho: se o cliente mandou voz e o TTS está ligado, tenta responder
	// com nota de voz. Qualquer falha cai (com log) para o envio de texto abaixo.
	//
	// O retorno é QUANTAS bolhas viraram áudio, contadas do começo — não um
	// booleano. Com clipes gravados só a primeira bolha costuma casar (é onde
	// mora a fala protocolar: "Perfeito!", "Só um instante"), e as seguintes
	// carregam o conteúdo específico daquele atendimento, que gravação nenhuma
	// diz. Mandar a primeira em voz e o resto em texto é o que o atendente faz.
	bolhasEmVoz := 0
	var duracaoAudio time.Duration
	if shouldReplyAsAudio(e.deps.Voice, cfg, inbound) && len(output.Bubbles) > 0 {
		bolhasEmVoz, duracaoAudio = e.trySendVoice(ctx, conv, inbound, output, reasoningJSON, cfg)
		if bolhasEmVoz == 0 {
			log.Info("voz falhou; caindo para texto", "wamid", wamid)
		}
	}

	if bolhasEmVoz < len(output.Bubbles) {
		// O que saiu em voz também conta para a pausa: quem acabou de ouvir uma
		// nota de voz não deve receber o texto seguinte no mesmo instante.
		primeiraDepoisDoAudio := bolhasEmVoz > 0
		for i, bubble := range output.Bubbles {
			if i < bolhasEmVoz {
				continue // já saiu como nota de voz
			}
			// O delay é o tempo de ESCREVER o balão que vem — por isso a conta
			// é sobre `bubble`, não sobre o anterior.
			var delay time.Duration
			switch {
			case primeiraDepoisDoAudio:
				// Quem acabou de gravar um áudio não começa a digitar no mesmo
				// segundo. Sem esta pausa os dois chegam juntos e o conjunto
				// denuncia automação mais do que o áudio gravado ajuda.
				delay = DepoisDoAudioDelayMs(duracaoAudio, bubble)
				primeiraDepoisDoAudio = false
			case i == 0:
				delay = FirstBubbleDelayMs(bubble)
			default:
				delay = BetweenBubblesDelayMs(bubble)
			}

			// "digitando…" durante a espera.
			//
			// O indicador do WhatsApp morre quando uma mensagem é enviada, então
			// depois do áudio ele sumiu e a pausa vira silêncio — que parece
			// conversa travada, não alguém escrevendo. Reexibir antes de cada
			// balão devolve o sinal de vida.
			//
			// O Cloud API só tem "digitando"; "gravando áudio" não existe na
			// API (testado: type=audio e type=recording violam o enum). Então a
			// nota de voz sai sem indicador nenhum — não há o que fazer daqui.
			if inbound.ProviderMessageID != "" {
				if err := e.deps.Sender.SendTypingIndicator(ctx, inbound.ProviderMessageID); err != nil {
					log.Debug("typing: não exibiu o indicador", "err", err)
				}
			}

			// Sleep acontece fora de qualquer transação
			e.deps.Sleep(delay)

			idempotencyKey := fmt.Sprintf("%s:bubble:%d", wamid, i)

			outboundMsg := OutboundMessage{
				TenantID:       inbound.TenantID,
				ConversationID: conv.ID,
				Channel:        inbound.Channel,
				To:             inbound.ExternalID,
				Intent:         IntentFreeForm,
				Content:        MessageContent{Type: "text", Text: bubble},
				IdempotencyKey: idempotencyKey,
			}

			// Apenas o primeiro balão carrega o reasoning.
			var bubbleReasoning *string
			if i == 0 {
				bubbleReasoning = reasoningJSON
			}

			// Envia e persiste dentro de uma transação individual por balão
			txErr := e.withTenant(ctx, func(tx pgx.Tx) error {
				providerMsgID, err := e.deps.Sender.SendMessage(ctx, outboundMsg)
				if err != nil {
					return fmt.Errorf("SendMessage bubble %d: %w", i, err)
				}

				// Persiste com ON CONFLICT DO NOTHING para exactly-once
				if err := e.deps.Messages.RecordOutbound(ctx, tx, idempotencyKey, inbound.TenantID, conv.ID, providerMsgID, bubble, bubbleReasoning); err != nil {
					return fmt.Errorf("RecordOutbound bubble %d: %w", i, err)
				}

				return nil
			})
			if txErr != nil {
				log.Error("erro ao enviar balão", "index", i, "err", txErr)
				return fmt.Errorf("engine.Handle (balão %d): %w", i, txErr)
			}

			// Broadcast do balão enviado (após commit da transação individual).
			if e.deps.Broadcast != nil {
				e.deps.Broadcast(WSEvent{Type: "message.outbound", ConversationID: conv.ID})
			}

		}
	}

	// -----------------------------------------------------------------------
	// Fase 2.5: modo admin — rascunha/envia respostas a clientes pendentes
	// -----------------------------------------------------------------------
	// Mesma guarda do kbEntry: clientActions/bookingActions são ferramentas de
	// admin e o parser as aceita de qualquer resposta do modelo.
	if cfg.IsAdminConversation {
		if len(output.ClientActions) > 0 {
			e.executeClientActions(ctx, inbound, output.ClientActions)
		}
		if len(output.BookingActions) > 0 {
			e.executeBookingActions(ctx, inbound, output.BookingActions)
		}
	} else if len(output.ClientActions) > 0 || len(output.BookingActions) > 0 {
		log.Warn("engine: ações de admin ignoradas fora de conversa admin",
			"clientActions", len(output.ClientActions),
			"bookingActions", len(output.BookingActions))
	}

	// -----------------------------------------------------------------------
	// Fase 3: atualiza estado do FSM e emite eventos pós-envio
	// -----------------------------------------------------------------------

	err = e.withTenant(ctx, func(tx pgx.Tx) error {
		// Recarrega conversa para ter estado atual
		convPtr, err := e.deps.Convs.FindByChannelIdentity(ctx, tx, conv.ChannelIdentityID)
		if err != nil {
			return fmt.Errorf("reload conversation: %w", err)
		}
		if convPtr != nil {
			conv = *convPtr
		}

		prevState := conv.State

		// n) Aplica transição do FSM
		newState := applyTransition(conv.State, output)

		// o) Handoff
		if output.Handoff {
			newState = StateHandoff
			handoffEvent := DomainEvent{
				TenantID:    inbound.TenantID,
				AggregateID: conv.ID,
				Type:        "notification.requested",
				Payload: map[string]any{
					"type":            "HANDOFF",
					"conversation_id": conv.ID,
					"contact_id":      conv.ContactID,
					// Canal de origem — define por qual sender o admin é notificado.
					"channel": inbound.Channel,
				},
				OccurredAt: time.Now(),
			}
			if err := e.deps.Emitter.Emit(ctx, tx, handoffEvent); err != nil {
				return fmt.Errorf("Emit notification.requested (handoff): %w", err)
			}

			// Registra a dúvida do cliente na fila para o admin responder depois.
			// Não aplica a conversas admin (admin não é um cliente esperando).
			if !cfg.IsAdminConversation && inboundText != "" && e.deps.Pending != nil {
				// inbound.Channel aqui é o canal do CLIENTE (handoff só ocorre fora de
				// conversa admin) — grava para rotear o reply pelo sender correto depois.
				if err := e.deps.Pending.Insert(ctx, tx, inbound.TenantID, conv.ID, contactPhone, contactName, inboundText, inbound.Channel); err != nil {
					return fmt.Errorf("Pending.Insert: %w", err)
				}
			}
		}

		// p) Reativação agendada
		if output.ScheduledContact != nil {
			if err := e.deps.Convs.SaveReactivation(ctx, tx, conv.ID, inbound.TenantID, conv.ContactID, output.ScheduledContact); err != nil {
				return fmt.Errorf("SaveReactivation: %w", err)
			}
		}

		// q) Gap na KB detectado: cliente fez uma pergunta factual respondida fora da
		// KB. Exclui conversa fiada (saudações) e handoff (que tem fluxo próprio).
		if !output.AnsweredFromKb && output.Answered && !output.Handoff && !output.Smalltalk {
			kbGapEvent := DomainEvent{
				TenantID:    inbound.TenantID,
				AggregateID: conv.ID,
				Type:        "kb.gap_detected",
				Payload: map[string]any{
					"conversation_id": conv.ID,
					"inbound_text":    inboundText,
					"answer_bubbles":  output.Bubbles,
					// Canal de origem — define por qual sender o admin é notificado.
					"channel": inbound.Channel,
				},
				OccurredAt: time.Now(),
			}
			if err := e.deps.Emitter.Emit(ctx, tx, kbGapEvent); err != nil {
				return fmt.Errorf("Emit kb.gap_detected: %w", err)
			}
		}

		// q2) Pedido de agendamento do cliente → grava pendência + notifica admin.
		if !cfg.IsAdminConversation && output.SchedulingRequest != nil && e.deps.Bookings != nil {
			sr := output.SchedulingRequest
			pb := PendingBooking{
				TenantID:       inbound.TenantID,
				ConversationID: conv.ID,
				ClientPhone:    contactPhone,
				ClientName:     contactName,
				StudentName:    sr.StudentName,
				Kind:           sr.Kind,
				Course:         sr.Course,
				ProposedDay:    sr.ProposedDay,
				ProposedDate:   sr.ProposedDate,
				ProposedTime:   sr.ProposedTime,
				ProposedPeriod: sr.ProposedPeriod,
				Age:            sr.Age,
				Notes:          sr.Notes,
				// Canal de origem do CLIENTE — define por qual sender o aviso ao cliente sai.
				Channel: inbound.Channel,
			}
			if err := e.deps.Bookings.Insert(ctx, tx, pb); err != nil {
				return fmt.Errorf("Bookings.Insert: %w", err)
			}
			name := sr.StudentName
			if name == "" {
				name = contactName
			}
			bookingEvent := DomainEvent{
				TenantID:    inbound.TenantID,
				AggregateID: conv.ID,
				Type:        "notification.requested",
				Payload: map[string]any{
					"type":            "BOOKING",
					"conversation_id": conv.ID,
					// Canal de origem — define por qual sender o admin é notificado.
					"channel": inbound.Channel,
					"message": fmt.Sprintf("%s — %s%s, proposto: %s %s %s. Responda aqui para confirmar, ajustar o horário ou recusar.",
						sr.Kind, name, courseSuffix(sr.Course), sr.ProposedDay, sr.ProposedTime, sr.ProposedPeriod),
				},
				OccurredAt: time.Now(),
			}
			if err := e.deps.Emitter.Emit(ctx, tx, bookingEvent); err != nil {
				return fmt.Errorf("Emit notification.requested (booking): %w", err)
			}
		}

		// r) Emite mudança de estado se necessário
		conv.State = newState
		if newState != prevState {
			stateEvent := DomainEvent{
				TenantID:    inbound.TenantID,
				AggregateID: conv.ID,
				Type:        "conversation.state_changed",
				Payload: map[string]any{
					"conversation_id": conv.ID,
					"from":            string(prevState),
					"to":              string(newState),
				},
				OccurredAt: time.Now(),
			}
			if err := e.deps.Emitter.Emit(ctx, tx, stateEvent); err != nil {
				return fmt.Errorf("Emit conversation.state_changed: %w", err)
			}
		}

		// s) Persiste conversa
		now := time.Now()
		conv.LastOutboundAt = &now
		if err := e.deps.Convs.Save(ctx, tx, conv); err != nil {
			return fmt.Errorf("Convs.Save: %w", err)
		}

		return nil
	})
	if err == nil && e.deps.Broadcast != nil {
		e.deps.Broadcast(WSEvent{Type: "conversation.updated", ConversationID: conv.ID})
	}

	// Grava log de processamento (assíncrono para não bloquear o fluxo).
	if inboundText != "" && e.deps.LogRepo != nil {
		answered := output.Answered
		answeredFromKb := output.AnsweredFromKb
		handoff := output.Handoff
		var toolCallsJSON json.RawMessage
		if len(output.ToolCalls) > 0 {
			toolCallsJSON, _ = json.Marshal(output.ToolCalls)
		}
		entry := ProcessingLogEntry{
			TenantID:       inbound.TenantID,
			ConversationID: conv.ID,
			ContactPhone:   contactPhone,
			ContactName:    contactName,
			InboundText:    inboundText,
			Answered:       &answered,
			AnsweredFromKb: &answeredFromKb,
			Handoff:        &handoff,
			CitedEntryIDs:  output.CitedEntryIDs,
			Bubbles:        output.Bubbles,
			ToolCalls:      toolCallsJSON,
			ProcessingMs:   int(time.Since(handleStart).Milliseconds()),
		}
		if err != nil {
			msg := err.Error()
			entry.Error = msg
		}
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					e.deps.Logger.Error("engine: panic ao gravar log de processamento", "panic", rec, "stack", string(debug.Stack()))
				}
			}()
			if insertErr := e.deps.LogRepo.Insert(context.Background(), entry); insertErr != nil {
				e.deps.Logger.Error("engine: falha ao gravar log de processamento", "err", insertErr)
				return
			}
			if e.deps.Broadcast != nil {
				dl := toDashLog(entry)
				e.deps.Broadcast(WSEvent{Type: "log.new", Log: &dl})
			}
		}()
	}

	// Agendamento sem intermediário — DEPOIS do commit e DEPOIS da resposta.
	//
	// Fora da transação de propósito: gravar no Notion é efeito externo, e se a
	// transação desse rollback a página ficaria lá, órfã, sem pendência no banco
	// para explicá-la.
	if err == nil && !cfg.IsAdminConversation && output.SchedulingRequest != nil {
		e.autoConfirmarAgendamento(ctx, conv, inbound, cfg, output.SchedulingRequest, contactName)
	}

	// Cliente desistiu: libera o horário para outra pessoa. Vale mesmo com o
	// auto-confirm desligado — desmarcar não cria nada, só devolve o que já
	// estava reservado.
	if err == nil && !cfg.IsAdminConversation && output.CancelaAula {
		e.cancelaAulaDoCliente(ctx, inbound)
	}

	return err
}

// autoConfirmarAgendamento grava a aula no Notion sem esperar um humano.
//
// Só roda quando AGENDA_AUTO_CONFIRM está ligado. Desligado, o comportamento é
// o de sempre: fica a pendência, alguém confirma.
//
// NÃO passa pelas bookingActions do LLM. Aquele caminho é guardado por
// IsAdminConversation, e abri-lo na conversa do cliente transformaria injeção
// de prompt em agendamento arbitrário: bastaria o cliente mandar uma mensagem
// fingindo ser instrução de sistema. Aqui, quem decide é o código — o modelo
// só propõe data e hora, e todas as travas são verificadas em Go.
func (e *ConversationEngine) autoConfirmarAgendamento(ctx context.Context, conv Conversation, inbound InboundMessage, cfg TenantConfig, sr *SchedulingRequest, contactName string) {
	log := e.deps.Logger
	if !e.deps.AgendaAutoConfirm || e.deps.Notion == nil || !e.deps.Notion.Enabled() {
		return
	}

	janela, err := ParseFuncionamento(cfg.EscolaAbre, cfg.EscolaFecha)
	if err != nil {
		log.Error("agenda: funcionamento mal configurado; não vou marcar sozinho", "err", err)
		return
	}
	dur := time.Duration(cfg.AulaDuracaoMin) * time.Minute
	if dur <= 0 {
		dur = time.Hour
	}

	// A data vem do modelo, então é conferida aqui: proposedDate (ISO) primeiro,
	// rótulo humano ("quinta") como último recurso.
	iso, ok := ResolveBookingDateTime(firstNonEmpty(sr.ProposedDate, sr.ProposedDay), sr.ProposedTime, time.Now())
	if !ok {
		log.Info("agenda: não consegui resolver a data proposta; fica a pendência",
			"dia", sr.ProposedDay, "data", sr.ProposedDate, "hora", sr.ProposedTime)
		return
	}
	inicio, ok := parseNotionTime(iso)
	if !ok {
		log.Error("agenda: data resolvida ilegível", "iso", iso)
		return
	}

	// Trava 1: agenda fresca. Cache não vale para decidir gravar.
	agenda, estado := e.deps.Notion.Schedule(ctx)
	if estado != AgendaOK {
		log.Warn("agenda: leitura não confiável; não vou marcar sozinho", "estado", estado)
		return
	}
	if motivo := PodeMarcar(inicio, dur, time.Now(), janela, agenda); motivo != "" {
		log.Info("agenda: horário recusado pelas travas", "motivo", string(motivo), "quando", iso)
		return
	}

	// Trava 2: última palavra, sem cache, imediatamente antes de gravar.
	if outro, ocupado, err := e.deps.Notion.SlotOcupado(ctx, inicio, dur); err != nil {
		log.Error("agenda: falha ao checar o horário; não vou marcar sozinho", "err", err)
		return
	} else if ocupado {
		log.Info("agenda: horário ocupado na checagem final", "quando", iso, "conflito_com", outro.Aluno)
		return
	}

	aluno := firstNonEmpty(sr.StudentName, contactName)
	pageID, err := e.deps.Notion.CreateBooking(ctx, Booking{
		Aluno:    aluno,
		WhatsApp: inbound.ExternalID,
		DataHora: iso,
		Status:   "Agendada",
		Tipo:     sr.Kind,
		Curso:    sr.Course,
		Idade:    sr.Age,
		Resumo:   sr.Notes,
	})
	if err != nil {
		log.Error("agenda: falha ao gravar no Notion", "err", err, "quando", iso)
		return
	}

	log.Info("agenda: aula marcada pelo bot", "aluno", aluno, "quando", iso, "conversa", conv.ID)
	e.avisaAdminsDoAgendamento(ctx, conv, inbound, aluno, sr, iso)
	// O Notion é o controle; o Google Agenda é o alarme. Falhar aqui não
	// desfaz a aula — ela existe e está avisada por WhatsApp.
	e.poeNoGoogleAgenda(ctx, pageID, aluno, sr, inicio, dur)
	e.agendaLembretesDoCliente(ctx, conv, inbound, pageID, aluno, inicio)
}

// avisaAdminsDoAgendamento manda o recado para quem opera a escola. É o que
// substitui o "alguém confirmou, então alguém sabe" que existia antes.
func (e *ConversationEngine) avisaAdminsDoAgendamento(ctx context.Context, conv Conversation, inbound InboundMessage, aluno string, sr *SchedulingRequest, iso string) {
	if e.deps.Emitter == nil {
		return
	}
	msg := fmt.Sprintf("✅ Aula experimental MARCADA pelo bot: %s%s — %s. Cliente: %s",
		aluno, courseSuffix(sr.Course), formatBRDateTime(iso), inbound.ExternalID)
	ev := DomainEvent{
		TenantID:    inbound.TenantID,
		AggregateID: conv.ID,
		Type:        "notification.requested",
		Payload: map[string]any{
			"type":            "BOOKING_CONFIRMED",
			"conversation_id": conv.ID,
			"channel":         inbound.Channel,
			"message":         msg,
		},
		OccurredAt: time.Now(),
	}
	if err := e.withTenant(ctx, func(tx pgx.Tx) error {
		return e.deps.Emitter.Emit(ctx, tx, ev)
	}); err != nil {
		e.deps.Logger.Error("agenda: falha ao avisar os admins", "err", err)
	}
}

// ---------------------------------------------------------------------------
// applyTransition — FSM de estado da conversa
// ---------------------------------------------------------------------------

// isBotEnabledFor decide se o bot deve estar ativo para um número recém-chegado,
// de acordo com a config do tenant. Se BotEnabledByDefault=false, o bot só é
// ativado para números presentes em BotAllowedNumbers.
func isBotEnabledFor(cfg TenantConfig, externalID string) bool {
	if cfg.BotEnabledByDefault {
		return true
	}
	for _, n := range cfg.BotAllowedNumbers {
		if n == externalID {
			return true
		}
	}
	return false
}

// applyTransition calcula o próximo estado da conversa dado o estado atual
// e a saída do Responder.
// executeClientActions processa as ações propostas pelo LLM no modo admin sobre
// dúvidas pendentes: grava rascunho (Send=false) ou envia ao cliente (Send=true),
// reengaja a conversa do cliente e resolve a pendência. Cada ação roda numa
// transação própria, fora da conversa do admin.
func (e *ConversationEngine) executeClientActions(ctx context.Context, inbound InboundMessage, actions []ClientAction) {
	log := e.deps.Logger
	if e.deps.Pending == nil || e.deps.Sender == nil {
		return
	}

	for _, act := range actions {
		if act.PendingID == "" {
			continue
		}

		// Carrega a pendência ainda aberta — guarda de idempotência.
		var pq *PendingQuestion
		if err := e.withTenant(ctx, func(tx pgx.Tx) error {
			loaded, e2 := e.deps.Pending.Get(ctx, tx, inbound.TenantID, act.PendingID)
			pq = loaded
			return e2
		}); err != nil {
			log.Error("clientAction: falha ao carregar pendência", "id", act.PendingID, "err", err)
			continue
		}
		if pq == nil {
			continue // já resolvida ou inexistente
		}

		draft := strings.TrimSpace(act.Draft)
		if draft == "" {
			draft = pq.Draft // reaproveita rascunho anterior se o envio veio sem texto
		}
		if draft == "" {
			continue
		}

		// Apenas rascunho: guarda e aguarda a confirmação do admin.
		if !act.Send {
			if err := e.withTenant(ctx, func(tx pgx.Tx) error {
				return e.deps.Pending.StoreDraft(ctx, tx, inbound.TenantID, pq.ID, draft)
			}); err != nil {
				log.Error("clientAction: falha ao guardar rascunho", "id", pq.ID, "err", err)
			}
			continue
		}

		// Envio confirmado: manda ao cliente, persiste, reengaja e resolve.
		// Usa o canal de ORIGEM DO CLIENTE (gravado na pendência), não o canal do
		// admin (inbound.Channel) — senão a resposta sairia pelo número errado.
		clientChannel := pq.Channel
		if clientChannel == "" {
			clientChannel = "whatsapp"
		}
		clientSender := e.senderFor(clientChannel)
		idemKey := fmt.Sprintf("pending:%s:reply", pq.ID)
		outboundMsg := OutboundMessage{
			TenantID:       inbound.TenantID,
			ConversationID: pq.ConversationID,
			Channel:        clientChannel,
			To:             pq.ClientPhone,
			Intent:         IntentFreeForm,
			Content:        MessageContent{Type: "text", Text: draft},
			IdempotencyKey: idemKey,
		}
		sendErr := e.withTenant(ctx, func(tx pgx.Tx) error {
			providerMsgID, err := clientSender.SendMessage(ctx, outboundMsg)
			if err != nil {
				return fmt.Errorf("SendMessage: %w", err)
			}
			if err := e.deps.Messages.RecordOutbound(ctx, tx, idemKey, inbound.TenantID, pq.ConversationID, providerMsgID, draft, nil); err != nil {
				return fmt.Errorf("RecordOutbound: %w", err)
			}
			if err := e.deps.Convs.SetState(ctx, tx, inbound.TenantID, pq.ConversationID, StateEngaged); err != nil {
				return fmt.Errorf("SetState: %w", err)
			}
			if err := e.deps.Pending.MarkResolved(ctx, tx, inbound.TenantID, pq.ID); err != nil {
				return fmt.Errorf("MarkResolved: %w", err)
			}
			return nil
		})
		if sendErr != nil {
			log.Error("clientAction: falha ao enviar ao cliente", "id", pq.ID, "err", sendErr)
			who := pq.ClientName
			if who == "" {
				who = pq.ClientPhone
			}
			_ = e.deps.Sender.SendText(ctx, inbound.ExternalID,
				fmt.Sprintf("⚠️ Não consegui enviar a resposta para %s agora. Tenta de novo daqui a pouco.", who))
			continue
		}

		if e.deps.Broadcast != nil {
			e.deps.Broadcast(WSEvent{Type: "message.outbound", ConversationID: pq.ConversationID})
		}
		log.Info("clientAction: resposta enviada ao cliente", "pendingID", pq.ID, "conv", pq.ConversationID)
	}
}

// startTypingIndicator exibe "digitando…" no WhatsApp e o mantém vivo (a API
// expira em ~25s) reenviando a cada 20s, até a função de parada ser chamada.
// Retorna uma função idempotente para encerrar o indicador.
func (e *ConversationEngine) startTypingIndicator(ctx context.Context, messageID string) func() {
	if e.deps.Sender == nil || messageID == "" {
		return func() {}
	}

	send := func() {
		if err := e.deps.Sender.SendTypingIndicator(ctx, messageID); err != nil {
			e.deps.Logger.Debug("typing indicator falhou", "err", err)
		}
	}
	send() // imediato

	done := make(chan struct{})
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				e.deps.Logger.Error("engine: panic no ticker de typing indicator", "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				send()
			}
		}
	}()

	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// executeBookingActions processa as decisões do admin sobre agendamentos: confirma
// (grava a aula no Notion + avisa o cliente), ajusta (re-propõe ao cliente) ou recusa.
func (e *ConversationEngine) executeBookingActions(ctx context.Context, inbound InboundMessage, actions []BookingAction) {
	log := e.deps.Logger
	if e.deps.Bookings == nil || e.deps.Sender == nil {
		return
	}

	for _, act := range actions {
		if act.BookingID == "" {
			continue
		}

		var pb *PendingBooking
		if err := e.withTenant(ctx, func(tx pgx.Tx) error {
			loaded, e2 := e.deps.Bookings.Get(ctx, tx, inbound.TenantID, act.BookingID)
			pb = loaded
			return e2
		}); err != nil {
			log.Error("bookingAction: falha ao carregar agendamento", "id", act.BookingID, "err", err)
			continue
		}
		if pb == nil {
			continue // já decidido ou inexistente
		}

		day := firstNonEmpty(act.Day, pb.ProposedDay)
		tm := firstNonEmpty(act.Time, pb.ProposedTime)

		switch act.Action {
		case "confirm":
			// Data exata: prioriza o override do admin, depois a data ISO que o LLM
			// calculou (proposedDate), e só por último o rótulo ("quinta") resolvido.
			// Se nada resolver, grava sem data e marca Status "Confirmar" pro admin.
			dateInput := firstNonEmpty(act.Day, pb.ProposedDate, pb.ProposedDay)
			dataHora, resolved := ResolveBookingDateTime(dateInput, tm, time.Now())

			// Grava no Notion (data source "Agenda — Aulas Experimentais"), se configurado.
			var notionErr error
			if e.deps.Notion != nil && e.deps.Notion.Enabled() {
				status := "Agendada"
				if !resolved {
					status = "Confirmar"
				}
				_, notionErr = e.deps.Notion.CreateBooking(ctx, Booking{
					Aluno:    bookingAluno(*pb),
					WhatsApp: pb.ClientPhone,
					DataHora: dataHora,
					Status:   status,
					// Contexto do atendimento: vai no corpo da página, para quem
					// for dar a aula chegar sabendo com quem vai falar. Era
					// coletado na conversa (schedulingRequest.notes) e morria no
					// banco — o Notion só recebia nome, telefone e horário.
					Tipo:   pb.Kind,
					Curso:  pb.Course,
					Idade:  pb.Age,
					Resumo: pb.Notes,
				})
				if notionErr != nil {
					log.Error("bookingAction: falha ao gravar no Notion", "id", pb.ID, "err", notionErr)
				}

				// Registra a ação no log do dashboard (fora do turno de conversa).
				quandoLog := firstNonEmpty(formatBRDateTime(dataHora), strings.TrimSpace(day+" "+tm))
				desc := fmt.Sprintf("📅 Agendamento no Notion: %s — %s%s, %s [%s]",
					bookingAluno(*pb), pb.Kind, courseSuffix(pb.Course), quandoLog, status)
				errStr := ""
				if notionErr != nil {
					errStr = notionErr.Error()
				}
				e.logAction(inbound.TenantID, pb.ConversationID, pb.ClientPhone, bookingAluno(*pb), desc, errStr)
			}
			// Marca confirmado e avisa o cliente.
			if err := e.withTenant(ctx, func(tx pgx.Tx) error {
				return e.deps.Bookings.MarkStatus(ctx, tx, inbound.TenantID, pb.ID, "confirmed")
			}); err != nil {
				log.Error("bookingAction: falha ao marcar confirmado", "id", pb.ID, "err", err)
			}
			// Tie-in CRM: avança o lead para 'aula_marcada'.
			if e.deps.Leads != nil {
				if err := e.withTenant(ctx, func(tx pgx.Tx) error {
					return e.deps.Leads.SetStatusByConversation(ctx, tx, inbound.TenantID, pb.ConversationID, "aula_marcada")
				}); err != nil {
					log.Error("bookingAction: falha ao avançar lead", "id", pb.ID, "err", err)
				}
			}
			// Prefere a data resolvida ("qui 30/07 às 19:00") na mensagem; senão o rótulo.
			quando := fmt.Sprintf("%s às %s", day, tm)
			if resolved {
				quando = formatBRDateTime(dataHora)
			}
			msg := fmt.Sprintf("Prontinho! Sua aula ficou marcada para %s. Qualquer coisa, é só me chamar. 😊", quando)
			if err := e.sendClientReply(ctx, inbound, pb.ConversationID, pb.ClientPhone, pb.Channel, "booking:"+pb.ID+":confirm", msg); err != nil {
				log.Error("bookingAction: falha ao avisar cliente", "id", pb.ID, "err", err)
			}
			if notionErr != nil {
				who := firstNonEmpty(pb.ClientName, pb.ClientPhone)
				_ = e.deps.Sender.SendText(ctx, inbound.ExternalID,
					fmt.Sprintf("⚠️ Confirmei com %s, mas não consegui gravar no Notion. Lance manualmente: %s %s.", who, day, tm))
			}

		case "adjust":
			msg := fmt.Sprintf("Consegui um horário: %s às %s. Pode ser pra você?", day, tm)
			if err := e.sendClientReply(ctx, inbound, pb.ConversationID, pb.ClientPhone, pb.Channel, fmt.Sprintf("booking:%s:adjust:%s%s", pb.ID, day, tm), msg); err != nil {
				log.Error("bookingAction: falha ao re-propor ao cliente", "id", pb.ID, "err", err)
			}

		case "reject":
			if err := e.withTenant(ctx, func(tx pgx.Tx) error {
				return e.deps.Bookings.MarkStatus(ctx, tx, inbound.TenantID, pb.ID, "rejected")
			}); err != nil {
				log.Error("bookingAction: falha ao marcar recusado", "id", pb.ID, "err", err)
			}
		}
	}
}

// sendClientReply envia uma mensagem ao cliente numa conversa específica (cross-conversa),
// persiste o balão, reengaja a conversa e faz broadcast. Usado por agendamentos.
// clientChannel é o canal de ORIGEM DO CLIENTE (whatsapp | evolution) — define por
// qual sender a mensagem sai, independentemente do canal do admin (inbound.Channel).
func (e *ConversationEngine) sendClientReply(ctx context.Context, inbound InboundMessage, convID ConversationID, toPhone, clientChannel, idemKey, text string) error {
	if clientChannel == "" {
		clientChannel = "whatsapp"
	}
	clientSender := e.senderFor(clientChannel)
	outboundMsg := OutboundMessage{
		TenantID:       inbound.TenantID,
		ConversationID: convID,
		Channel:        clientChannel,
		To:             toPhone,
		Intent:         IntentFreeForm,
		Content:        MessageContent{Type: "text", Text: text},
		IdempotencyKey: idemKey,
	}
	err := e.withTenant(ctx, func(tx pgx.Tx) error {
		providerMsgID, err := clientSender.SendMessage(ctx, outboundMsg)
		if err != nil {
			return fmt.Errorf("SendMessage: %w", err)
		}
		if err := e.deps.Messages.RecordOutbound(ctx, tx, idemKey, inbound.TenantID, convID, providerMsgID, text, nil); err != nil {
			return fmt.Errorf("RecordOutbound: %w", err)
		}
		if err := e.deps.Convs.SetState(ctx, tx, inbound.TenantID, convID, StateEngaged); err != nil {
			return fmt.Errorf("SetState: %w", err)
		}
		return nil
	})
	if err == nil && e.deps.Broadcast != nil {
		e.deps.Broadcast(WSEvent{Type: "message.outbound", ConversationID: convID})
	}
	return err
}

// logAction grava uma AÇÃO do sistema no log (kind="action"), fora de um turno de
// conversa, e transmite pro dashboard em tempo real. Best-effort (async).
func (e *ConversationEngine) logAction(tenantID TenantID, convID ConversationID, phone, name, description, errStr string) {
	if e.deps.LogRepo == nil {
		return
	}
	entry := ProcessingLogEntry{
		Kind:           "action",
		TenantID:       tenantID,
		ConversationID: convID,
		ContactPhone:   phone,
		ContactName:    name,
		InboundText:    description,
		Error:          errStr,
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil && e.deps.Logger != nil {
				e.deps.Logger.Error("engine: panic ao gravar log de ação", "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		if err := e.deps.LogRepo.Insert(context.Background(), entry); err != nil {
			if e.deps.Logger != nil {
				e.deps.Logger.Error("engine: falha ao gravar log de ação", "err", err)
			}
			return
		}
		if e.deps.Broadcast != nil {
			dl := toDashLog(entry)
			e.deps.Broadcast(WSEvent{Type: "log.new", Log: &dl})
		}
	}()
}

// bookingAluno monta o "Aluno/Responsável" gravado no Notion: o nome informado na
// conversa com o nome do perfil do WhatsApp entre parênteses, ex.: "Guilherme (moto
// da apple)". Se só houver um dos dois, usa o que tiver; sem nenhum, cai no telefone.
func bookingAluno(pb PendingBooking) string {
	student := strings.TrimSpace(pb.StudentName)
	wpp := strings.TrimSpace(pb.ClientName)
	switch {
	case student != "" && wpp != "" && !strings.EqualFold(student, wpp):
		return student + " (" + wpp + ")"
	case student != "":
		return student
	case wpp != "":
		return wpp
	default:
		return pb.ClientPhone
	}
}

// courseSuffix devolve ", curso X" quando há curso, ou string vazia.
func courseSuffix(course string) string {
	if course == "" {
		return ""
	}
	return ", " + course
}

// firstNonEmpty retorna o primeiro valor não-vazio.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// shouldReplyAsAudio: responde em áudio só quando o cliente mandou voz, o TTS
// está ligado no ambiente E o tenant não desligou a voz no painel.
func shouldReplyAsAudio(v *VoiceClient, cfg TenantConfig, inbound InboundMessage) bool {
	return inbound.WasVoice && v.Enabled() && cfg.VoiceEnabled
}

// trySendVoice gera UMA nota de voz com a resposta inteira e envia pelo Meta.
// Retorna false (com log) em qualquer falha → chamador cai no texto.
// Devolve quantas bolhas viraram áudio e a DURAÇÃO do que foi enviado — a
// duração alimenta a pausa antes do texto seguinte.
func (e *ConversationEngine) trySendVoice(ctx context.Context, conv Conversation, inbound InboundMessage, output ResponderOutput, reasoningJSON *string, cfg TenantConfig) (int, time.Duration) {
	log := e.deps.Logger
	if _, ok := e.deps.Sender.(*WhatsAppSender); !ok {
		return 0, 0 // só Meta suporta upload de mídia
	}
	text := joinBubbles(output.Bubbles)
	sel := VoiceSelection{Provider: cfg.VoiceProvider, VoiceID: cfg.VoiceID, Model: cfg.VoiceModel}

	// Provider 'clips': a voz do atendente não pode mais ser sintetizada (o plano
	// de TTS acabou), então só existe o acervo gravado. Sem clipe para este
	// momento, a escolha deliberada é responder em TEXTO em vez de usar outra
	// voz — o cliente ouve o atendente ou lê, nunca um estranho no meio da
	// conversa. A lacuna fica registrada para virar gravação depois.
	if sel.Provider == "clips" {
		voice := sel.VoiceID
		if voice == "" {
			voice = "henrique"
		}
		if e.deps.AudioClips == nil || !e.deps.AudioClips.Enabled() {
			log.Warn("clips: banco de áudios não configurado; caindo para texto")
			return 0, 0
		}

		// Casa contra a PRIMEIRA bolha, não contra a resposta inteira.
		//
		// O bot responde em duas mensagens: a primeira é a fala protocolar
		// ("Opa, tudo certo por aqui!") e a segunda carrega o conteúdo daquele
		// atendimento ("qual dia fica melhor pra você?"). Juntar as duas e
		// procurar UMA gravação que diga tudo não acha nada — e nem chega a
		// pontuar, porque os fatos da segunda bolha (dia, curso, idade) não
		// existem em nenhum clipe. Medido em produção: 0 clipes considerados
		// na frase junta, 30 na primeira bolha isolada.
		alvo := output.Bubbles[0]
		clip, info, cerr := e.deps.AudioClips.MatchAnswer(ctx, inbound.TenantID, voice, alvo, MatchOpts{
			MinScore:     e.deps.AudioMatchMin,
			MaxDuracaoMs: e.deps.AudioMatchMaxMs,
			ConversaNova: conv.State == StateNew,
		})
		if cerr != nil {
			log.Error("clips: casamento", "err", cerr)
			return 0, 0
		}

		// Modo sombra: decide e registra, mas ainda responde em texto. É como se
		// mede a taxa de acerto em produção antes de deixar o áudio sair.
		if clip != nil && e.deps.AudioMatchShadow {
			log.Info("clips: SOMBRA — casaria e não enviou",
				"voice", voice, "intent", clip.IntentKey, "variante", clip.Variant,
				"score", info.Score, "candidatos", info.Candidatos)
			return 0, 0
		}

		if clip == nil {
			// A lacuna é gravada SÓ quando falta áudio — antes era registrada em
			// toda mensagem, o que enchia a fila de gravação de falas que já
			// existem. O near miss entra junto: para quem vai gravar, "ficou
			// perto de conv_experimental" vale muito mais que uma chave vazia.
			if gerr := e.deps.AudioClips.RecordGap(ctx, inbound.TenantID, voice, info.NearMiss, alvo); gerr != nil {
				// Best-effort: perder o registro não pode travar a resposta.
				log.Warn("clips: falha ao registrar lacuna", "err", gerr)
			}
			log.Info("clips: nenhuma gravação diz isto; respondendo em texto",
				"voice", voice, "near_miss", info.NearMiss, "melhor_score", info.Score,
				"considerados", info.Considerados)
			return 0, 0
		}

		data, rerr := e.deps.AudioClips.Read(clip)
		if rerr != nil {
			log.Error("clips: leitura do arquivo", "err", rerr, "file", clip.FilePath)
			return 0, 0
		}
		log.Info("clips: nota de voz enviada",
			"voice", voice, "intent", clip.IntentKey, "variante", clip.Variant,
			"score", info.Score, "candidatos", info.Candidatos,
			"bolhas_restantes", len(output.Bubbles)-1)
		if !e.sendVoiceBytes(ctx, conv, inbound, data, clip.Transcript, reasoningJSON) {
			return 0, 0
		}
		// só a primeira bolha; o resto segue em texto
		return 1, time.Duration(clip.DurationMs) * time.Millisecond
	}

	ogg, err := e.deps.Voice.Synthesize(ctx, text, sel)
	if err != nil && sel.Provider == "elevenlabs" {
		// A voz escolhida falhou (quota, plano sem clonagem, voz apagada). Em vez
		// de devolver texto a quem mandou áudio, tenta o OpenAI para preservar o
		// espelhamento de mídia. Fica no log porque é troca de voz silenciosa —
		// o cliente ouve alguém diferente do configurado.
		log.Warn("tts: voz escolhida falhou; caindo para o provedor padrão",
			"provider", sel.Provider, "voice_id", sel.VoiceID, "err", err)
		ogg, err = e.deps.Voice.Synthesize(ctx, text, VoiceSelection{})
	}
	if err != nil {
		log.Error("tts: synthesize", "err", err)
		return 0, 0
	}
	// O TTS sintetiza a resposta INTEIRA numa nota só — diferente dos clipes,
	// ele não depende do que já existe gravado. Consome todas as bolhas.
	if !e.sendVoiceBytes(ctx, conv, inbound, ogg, text, reasoningJSON) {
		return 0, 0
	}
	// O TTS sintetiza tudo numa nota só; não há texto depois, então a duração
	// não é usada.
	return len(output.Bubbles), 0
}

// sendVoiceBytes faz o upload do OGG e envia como nota de voz, gravando a
// mensagem na mesma transação. Compartilhado pelos dois caminhos (TTS e clipe
// pré-gravado), que só diferem em COMO o áudio foi obtido.
//
// `transcript` é o que o áudio fala — vai para o histórico, para o bot lembrar
// depois do que ele mesmo disse.
func (e *ConversationEngine) sendVoiceBytes(ctx context.Context, conv Conversation, inbound InboundMessage, ogg []byte, transcript string, reasoningJSON *string) bool {
	log := e.deps.Logger
	ms, ok := e.deps.Sender.(*WhatsAppSender)
	if !ok {
		return false
	}
	mediaID, err := ms.UploadAudio(ctx, ogg)
	if err != nil {
		log.Error("voz: upload", "err", err)
		return false
	}
	ref := "wa_media_id:" + mediaID
	idemKey := fmt.Sprintf("%s:voice", inbound.ProviderMessageID)
	out := OutboundMessage{
		TenantID:       inbound.TenantID,
		ConversationID: conv.ID,
		Channel:        inbound.Channel,
		To:             inbound.ExternalID,
		Intent:         IntentFreeForm,
		Content:        MessageContent{Type: "audio", MediaURL: &ref, Transcript: &transcript},
		IdempotencyKey: idemKey,
	}
	txErr := e.withTenant(ctx, func(tx pgx.Tx) error {
		providerMsgID, serr := ms.SendMessage(ctx, out)
		if serr != nil {
			return serr
		}
		return e.deps.Messages.RecordOutbound(ctx, tx, idemKey, inbound.TenantID, conv.ID, providerMsgID, transcript, reasoningJSON)
	})
	if txErr != nil {
		log.Error("voz: enviar/gravar", "err", txErr)
		return false
	}
	if e.deps.Broadcast != nil {
		e.deps.Broadcast(WSEvent{Type: "message.outbound", ConversationID: conv.ID})
	}
	return true
}

func applyTransition(current ConversationState, output ResponderOutput) ConversationState {
	switch current {
	case StateNew:
		// Primeira resposta: sempre avança para ENGAGED
		return StateEngaged

	case StateEngaged:
		if output.Handoff {
			return StateHandoff
		}
		if output.Answered && output.AnsweredFromKb {
			return StateConcludedPositive
		}
		if output.Answered && !output.AnsweredFromKb {
			// Respondeu, mas sem KB → aguarda complemento humano
			return StateAwaitingReply
		}
		return StateEngaged

	case StateAwaitingReply:
		// Nova mensagem chegou → volta para engajado (resetado pelo Handle)
		return StateEngaged

	case StateHandoff:
		// Só humano pode mudar este estado
		return StateHandoff

	default:
		return current
	}
}

// poeNoGoogleAgenda cria o evento na agenda de cada pessoa que autorizou.
//
// Best-effort de propósito: o Notion é o controle e o WhatsApp já avisou. Se o
// Google falhar, a aula continua marcada e a falha fica registrada na conta —
// o inverso (desfazer a aula porque a agenda não respondeu) seria pior.
func (e *ConversationEngine) poeNoGoogleAgenda(ctx context.Context, notionPageID, aluno string, sr *SchedulingRequest, inicio time.Time, dur time.Duration) {
	log := e.deps.Logger
	if e.deps.GCal == nil || !e.deps.GCal.Enabled() || e.deps.GCalRepo == nil || notionPageID == "" {
		return
	}
	contas, err := e.deps.GCalRepo.Ativas(ctx, e.deps.TenantID)
	if err != nil {
		log.Error("gcal: falha ao listar contas", "err", err)
		return
	}
	if len(contas) == 0 {
		return // ninguém autorizou ainda
	}

	ev := EventoAula{
		Titulo:    TituloAgendamento(aluno),
		Descricao: descricaoDoEvento(sr),
		Inicio:    inicio,
		Fim:       inicio.Add(dur),
	}
	for _, c := range contas {
		eventID, err := e.deps.GCal.CriarEvento(ctx, c.RefreshToken, c.CalendarID, ev)
		if err != nil {
			log.Error("gcal: falha ao criar evento", "err", err, "conta", c.Email)
			e.deps.GCalRepo.RegistrarErro(ctx, c.ID, err.Error())
			continue
		}
		if err := e.deps.GCalRepo.VincularEvento(ctx, e.deps.TenantID, c.ID, notionPageID, eventID, inicio); err != nil {
			log.Error("gcal: evento criado mas não vinculado", "err", err, "conta", c.Email)
		}
		log.Info("gcal: evento criado", "conta", c.Email, "aluno", aluno, "quando", inicio.Format(time.RFC3339))
	}
}

// descricaoDoEvento — o que quem abrir o evento na agenda precisa saber antes
// de dar a aula.
func descricaoDoEvento(sr *SchedulingRequest) string {
	var b strings.Builder
	b.WriteString("Aula experimental marcada pelo bot.\n\n")
	if sr.Course != "" {
		fmt.Fprintf(&b, "Curso de interesse: %s\n", sr.Course)
	}
	if sr.Age > 0 {
		fmt.Fprintf(&b, "Idade do aluno: %d\n", sr.Age)
	}
	if sr.Notes != "" {
		fmt.Fprintf(&b, "\nResumo do atendimento:\n%s\n", sr.Notes)
	}
	b.WriteString("\nO controle continua no Notion.")
	return b.String()
}

// agendaLembretesDoCliente cria os três avisos: véspera, 4 horas e 1 hora.
//
// Best-effort como o Google Agenda: a aula já está marcada e o cliente já foi
// respondido. Falhar aqui custa um lembrete, não a aula.
func (e *ConversationEngine) agendaLembretesDoCliente(ctx context.Context, conv Conversation, inbound InboundMessage, notionPageID, aluno string, aulaEm time.Time) {
	if e.deps.Lembretes == nil || notionPageID == "" {
		return
	}
	n, err := e.deps.Lembretes.Agendar(ctx, inbound.TenantID, notionPageID,
		string(conv.ID), inbound.ExternalID, inbound.Channel, aluno, aulaEm, time.Now())
	if err != nil {
		e.deps.Logger.Error("lembretes: falha ao agendar", "err", err, "aula", notionPageID)
		return
	}
	e.deps.Logger.Info("lembretes: agendados", "quantos", n, "aluno", aluno,
		"aula_em", aulaEm.Format(time.RFC3339))
}

// cancelaAulaDoCliente libera o horário quando o cliente diz que não vai.
//
// Acha a aula pela conversa: o telefone do cliente está na agenda, e a próxima
// aula futura dele é a que ele está desmarcando. Procurar por telefone em vez
// de guardar o page_id na conversa é de propósito — a aula pode ter sido
// remarcada pelo painel, e o telefone continua sendo o mesmo.
//
// Só mexe no que tem o marcador. Se a aula foi lançada à mão por Henrique ou
// Rodrigo, o bot avisa e não toca: quem marcou na mão desmarca na mão.
func (e *ConversationEngine) cancelaAulaDoCliente(ctx context.Context, inbound InboundMessage) {
	log := e.deps.Logger
	if e.deps.Notion == nil || !e.deps.Notion.Enabled() {
		return
	}
	agenda, estado := e.deps.Notion.Schedule(ctx)
	if estado == AgendaIndisponivel {
		log.Warn("cancelamento: agenda indisponível; não vou mexer")
		return
	}

	telefone := onlyDigits(inbound.ExternalID)
	agora := time.Now()
	var alvo *ScheduleEntry
	for i := range agenda {
		e2 := &agenda[i]
		if onlyDigits(e2.WhatsApp) != telefone || e2.WhatsApp == "" {
			continue
		}
		t, ok := parseNotionTime(e2.DataHora)
		if !ok || !t.After(agora) {
			continue
		}
		// A mais próxima no futuro é a que ele está desmarcando.
		if alvo == nil {
			alvo = e2
			continue
		}
		if tAlvo, ok := parseNotionTime(alvo.DataHora); ok && t.Before(tAlvo) {
			alvo = e2
		}
	}
	if alvo == nil {
		log.Info("cancelamento: nenhuma aula futura encontrada para este telefone", "de", inbound.ExternalID)
		return
	}
	if !EhAulaExperimental(alvo.Aluno) {
		log.Warn("cancelamento: a aula não é do bot; deixando como está",
			"titulo", alvo.Aluno, "quando", alvo.Display)
		return
	}

	if err := e.deps.Notion.ArquivarBooking(ctx, alvo.PageID); err != nil {
		log.Error("cancelamento: falha ao arquivar", "err", err, "aula", alvo.Aluno)
		return
	}
	if e.deps.Lembretes != nil {
		if n, err := e.deps.Lembretes.CancelarDaAula(ctx, inbound.TenantID, alvo.PageID); err == nil && n > 0 {
			log.Info("cancelamento: lembretes cancelados", "quantos", n)
		}
	}
	e.tiraDoGoogleAgenda(ctx, inbound.TenantID, alvo.PageID)
	log.Info("cancelamento: horário liberado", "aula", alvo.Aluno, "quando", alvo.Display)
}

// tiraDoGoogleAgenda apaga os eventos da aula nas agendas e esquece o vínculo.
func (e *ConversationEngine) tiraDoGoogleAgenda(ctx context.Context, tenantID TenantID, notionPageID string) {
	if e.deps.GCal == nil || !e.deps.GCal.Enabled() || e.deps.GCalRepo == nil {
		return
	}
	eventos, err := e.deps.GCalRepo.EventosDaAula(ctx, tenantID, notionPageID)
	if err != nil {
		e.deps.Logger.Error("gcal: falha ao buscar eventos da aula", "err", err)
		return
	}
	for _, ev := range eventos {
		if err := e.deps.GCal.ApagarEvento(ctx, ev.Conta.RefreshToken, ev.Conta.CalendarID, ev.EventID); err != nil {
			e.deps.Logger.Warn("gcal: falha ao apagar evento", "err", err, "conta", ev.Conta.Email)
		}
	}
	e.deps.GCalRepo.EsquecerAula(ctx, tenantID, notionPageID)
}
