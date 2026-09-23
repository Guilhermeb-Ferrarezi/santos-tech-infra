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
	// Qualificacoes — o dossiê de cada pessoa, lido antes de responder e
	// gravado depois. É a memória que o bot não tinha.
	Qualificacoes *QualificacaoRepo
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
		contactID     ContactID // sai da transação para o dossiê ser gravado depois
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
		contactID = contact.ID

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
		// O dossiê da PESSOA entra em toda mensagem. É o que faz o bot saber que
		// o filho se chama Caio, tem 14 anos e curte programação sem perguntar
		// pela terceira vez — e é o que dá o "como foi a aula do Caio?" quando a
		// família volta semanas depois, numa conversa nova.
		if e.deps.Qualificacoes != nil && !cfg.IsAdminConversation {
			convCtx.Qualificacao = e.deps.Qualificacoes.Get(ctx, inbound.TenantID, contact.ID)
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
		//
		// Só vale com o cliente tendo ACEITADO o horário. Horário que o bot
		// apenas ofereceu não é pedido de ninguém: gerava uma pendência por
		// proposta e enchia o painel da escola de aulas que o cliente nunca
		// pediu — quatro numa conversa só.
		if !cfg.IsAdminConversation && output.SchedulingRequest != nil &&
			output.SchedulingRequest.ClienteConfirmou && e.deps.Bookings != nil {
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
	// Dossiê da pessoa — grava o que se descobriu ANTES de tentar agendar, para
	// que uma falha no Notion não leve junto o que o cliente contou. O que ele
	// disse sobre si é dele; a aula é outro assunto.
	if err == nil && !cfg.IsAdminConversation && e.deps.Qualificacoes != nil && output.Qualificacao != nil {
		atual := convCtx.Qualificacao.Merge(*output.Qualificacao)
		if saveErr := e.deps.Qualificacoes.Save(ctx, inbound.TenantID, contactID, atual); saveErr != nil {
			e.deps.Logger.Error("qualificacao: falha ao gravar o dossiê", "err", saveErr, "contato", contactID)
		} else {
			e.deps.Logger.Info("qualificacao: dossiê atualizado",
				"contato", contactID, "respondidas", atual.Respondidas(),
				"grau", string(atual.Grau()), "pode_falar_preco", atual.PodeFalarPreco())
		}
	}

	marcouAgora := false
	if err == nil && !cfg.IsAdminConversation && output.SchedulingRequest != nil {
		marcouAgora = e.autoConfirmarAgendamento(ctx, conv, inbound, cfg, output.SchedulingRequest, contactName)
	}

	// Cliente desistiu: libera o horário para outra pessoa. Vale mesmo com o
	// auto-confirm desligado — desmarcar não cria nada, só devolve o que já
	// estava reservado.
	//
	// MAS não quando o bot acabou de marcar nesta mesma mensagem. "Não vou
	// conseguir quarta, pode ser quinta 17h?" aciona as duas coisas de uma vez:
	// o modelo marca cancelaAula (desistiu do que estava marcado) e manda o
	// pedido do horário novo (o cliente aceitou). Rodando os dois em sequência,
	// o cancelamento achava a aula RECÉM-CRIADA — a antiga já tinha saído — e
	// arquivava também. O cliente lia "remarcado para quinta" e ficava sem aula
	// nenhuma. Remarcar já é cancelar e marcar; fazer o cancelamento de novo por
	// cima só destrói.
	if err == nil && !cfg.IsAdminConversation && output.CancelaAula && !marcouAgora {
		e.cancelaAulaDoCliente(ctx, conv, inbound)
	}

	// Gmail do cliente: caminho próprio, que NÃO toca na aula.
	//
	// O bot pede o e-mail depois de marcar, então ele chega numa mensagem que
	// não fala de horário nenhum. A primeira versão fazia o modelo reemitir o
	// pedido de agendamento inteiro só para carregar o endereço — e aí qualquer
	// imprecisão na repetição ("quinta", sem data) virava remarcação silenciosa,
	// pendência duplicada no painel e alarme falso para os admins. Convidar na
	// agenda não precisa saber a data: a aula já existe e o bot sabe qual é.
	if err == nil && !cfg.IsAdminConversation && output.ClienteEmail != "" {
		e.convidaClienteDaConversa(ctx, conv, inbound, output.ClienteEmail)
	}

	return err
}

// convidaClienteDaConversa põe o cliente no evento da aula que ele já tem.
func (e *ConversationEngine) convidaClienteDaConversa(ctx context.Context, conv Conversation, inbound InboundMessage, email string) {
	log := e.deps.Logger
	valido := GmailValido(email)
	if valido == "" {
		// Não é erro do sistema: o cliente mandou um endereço que não serve, ou
		// a transcrição do áudio embaralhou. Fica no log para dar para ver que
		// o pedido do Gmail está rendendo endereço ruim.
		log.Info("gcal: endereço informado não serve para convite (precisa ser Gmail)",
			"de", inbound.ExternalID)
		return
	}
	if e.deps.Lembretes == nil {
		return
	}
	aula, ok := e.deps.Lembretes.AulaDaConversa(ctx, inbound.TenantID, string(conv.ID))
	if !ok {
		log.Info("gcal: cliente mandou e-mail mas não tem aula marcada nesta conversa",
			"de", inbound.ExternalID)
		return
	}
	e.convidaClienteNoEvento(ctx, aula.PageID, valido)
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
func (e *ConversationEngine) autoConfirmarAgendamento(ctx context.Context, conv Conversation, inbound InboundMessage, cfg TenantConfig, sr *SchedulingRequest, contactName string) (marcou bool) {
	log := e.deps.Logger
	if !e.deps.AgendaAutoConfirm || e.deps.Notion == nil || !e.deps.Notion.Enabled() {
		return false
	}

	// Trava 0: o cliente precisa ter aceitado.
	//
	// É a trava que faltava. Sem ela, propor e marcar eram a mesma coisa: o bot
	// gravava o horário que tinha acabado de oferecer, lia a própria aula como
	// ocupada na mensagem seguinte e pedia desculpa pela "confusão" — de novo e
	// de novo, três aulas fantasma, e o cliente sem horário nenhum no fim.
	if !sr.ClienteConfirmou {
		log.Info("agenda: cliente ainda não aceitou o horário; não marco",
			"proposto", sr.ProposedDay+" "+sr.ProposedTime, "conversa", conv.ID)
		return false
	}

	// Daqui para baixo o cliente JÁ aceitou, e o bot muito provavelmente já
	// disse a ele que está marcado — as bolhas saem antes desta função rodar.
	// Então toda desistência daqui em diante é uma promessa quebrada, e alguém
	// da escola precisa saber HOJE. Sem isto, a aula simplesmente não existia e
	// ninguém ficava sabendo: foi assim que o cliente ouviu "tá marcado sim" de
	// uma aula que nunca entrou na agenda.
	motivoFalha := ""
	defer func() {
		if !marcou && motivoFalha != "" {
			e.avisaAdminsDeAgendamentoFalho(ctx, conv, inbound,
				NomeDoAluno(sr.StudentName, contactName), sr, motivoFalha)
		}
	}()

	janela, err := ParseFuncionamento(cfg.EscolaAbre, cfg.EscolaFecha)
	if err != nil {
		motivoFalha = "o horário de funcionamento da escola está mal configurado no sistema"
		log.Error("agenda: funcionamento mal configurado; não vou marcar sozinho", "err", err)
		return false
	}
	dur := time.Duration(cfg.AulaDuracaoMin) * time.Minute
	if dur <= 0 {
		dur = time.Hour
	}

	// A data vem do modelo, então é conferida aqui: proposedDate (ISO) primeiro,
	// rótulo humano ("quinta") como último recurso.
	iso, ok := ResolveBookingDateTime(firstNonEmpty(sr.ProposedDate, sr.ProposedDay), sr.ProposedTime, time.Now())
	if !ok {
		motivoFalha = "não consegui entender a data/hora combinada"
		log.Info("agenda: não consegui resolver a data proposta; fica a pendência",
			"dia", sr.ProposedDay, "data", sr.ProposedDate, "hora", sr.ProposedTime)
		return false
	}
	inicio, ok := parseNotionTime(iso)
	if !ok {
		motivoFalha = "a data combinada saiu ilegível"
		log.Error("agenda: data resolvida ilegível", "iso", iso)
		return false
	}

	aluno := NomeDoAluno(sr.StudentName, contactName)

	// Uma conversa, uma aula POR ALUNO.
	//
	// Se este aluno já tem aula marcada pelo bot nesta conversa, a segunda
	// confirmação é REMARCAÇÃO, não uma aula a mais. Sem isto, "na verdade
	// prefiro quinta" deixaria as duas de pé: a escola veria duas aulas para a
	// mesma família e o horário antigo ficaria bloqueado para sempre.
	//
	// "Por aluno" não é detalhe: numa família com dois filhos as duas aulas
	// saem da MESMA conversa, e tratar a segunda como remarcação arquivaria a
	// aula do primeiro. Nome diferente => aula nova, sem mexer no que existe.
	var anterior AulaMarcada
	temAnterior := false
	if e.deps.Lembretes != nil {
		if a, ok := e.deps.Lembretes.AulaDaConversa(ctx, inbound.TenantID, string(conv.ID)); ok {
			if MesmoAluno(a.Aluno, aluno) {
				anterior, temAnterior = a, true
			} else {
				log.Info("agenda: a conversa já tem aula de outro aluno; esta é adicional",
					"ja_tem", a.Aluno, "agora", aluno)
			}
		}
	}
	if temAnterior && anterior.Em.Equal(inicio) {
		log.Info("agenda: esta aula já está marcada; nada a remarcar",
			"aula", anterior.PageID, "quando", iso, "conversa", conv.ID)
		return false
	}

	// Trava 1: agenda fresca. Cache não vale para decidir gravar.
	agenda, estado := e.deps.Notion.Schedule(ctx)
	if estado != AgendaOK {
		motivoFalha = "não consegui ler a agenda do Notion na hora de gravar"
		log.Warn("agenda: leitura não confiável; não vou marcar sozinho", "estado", estado)
		return false
	}
	if temAnterior {
		agenda = semAPagina(agenda, anterior.PageID)
	}
	if motivo := PodeMarcar(inicio, dur, time.Now(), janela, agenda); motivo != "" {
		motivoFalha = "o horário combinado não passou nas travas (passado, fora do funcionamento ou pouca antecedência)"
		log.Info("agenda: horário recusado pelas travas", "motivo", string(motivo), "quando", iso)
		return false
	}

	// Trava 2: última palavra, sem cache, imediatamente antes de gravar.
	//
	// A aula anterior sai da agenda ANTES da checagem, não depois. Escrito ao
	// contrário — checar tudo e depois perdoar se o conflito devolvido for o
	// dela — o bot marcava em cima de aluno real sempre que DOIS compromissos
	// pegassem o horário: Conflito() devolve só o primeiro, e bastava ele ser
	// a aula do próprio cliente para o segundo passar despercebido.
	ignorar := ""
	if temAnterior {
		ignorar = anterior.PageID
	}
	if outro, ocupado, err := e.deps.Notion.SlotOcupadoExceto(ctx, inicio, dur, ignorar); err != nil {
		motivoFalha = "não consegui conferir se o horário estava livre"
		log.Error("agenda: falha ao checar o horário; não vou marcar sozinho", "err", err)
		return false
	} else if ocupado {
		motivoFalha = "o horário foi ocupado por outra aula antes de eu gravar"
		log.Info("agenda: horário ocupado na checagem final", "quando", iso, "conflito_com", outro.Display())
		return false
	}

	// A aula velha só sai DEPOIS de o horário novo passar por todas as travas.
	// Arquivar antes deixaria o cliente sem aula nenhuma se o novo fosse
	// recusado — perder a aula que existia é pior que não conseguir remarcar.
	if temAnterior {
		if err := e.deps.Notion.ArquivarBooking(ctx, anterior.PageID); err != nil {
			motivoFalha = "não consegui tirar a aula anterior da agenda para remarcar"
			log.Error("agenda: falha ao tirar a aula anterior; não vou criar a nova",
				"err", err, "aula", anterior.PageID)
			return false
		}
		// Se este UPDATE falhar, a conversa continua "tendo" uma aula que já
		// foi arquivada — e é dele que sai a decisão de remarcar da próxima
		// vez. Engolir o erro em silêncio escondia justamente isso.
		if n, err := e.deps.Lembretes.CancelarDaAula(ctx, inbound.TenantID, anterior.PageID); err != nil {
			log.Error("agenda: aula anterior arquivada mas os lembretes dela continuam vivos",
				"err", err, "aula", anterior.PageID)
		} else if n > 0 {
			log.Info("agenda: lembretes da aula anterior cancelados", "quantos", n)
		}
		e.tiraDoGoogleAgenda(ctx, inbound.TenantID, anterior.PageID)
		log.Info("agenda: remarcando", "de", anterior.Em.Format(time.RFC3339), "para", iso)
	}

	pageID, err := e.deps.Notion.CreateBooking(ctx, Booking{
		Aluno:      aluno,
		WhatsApp:   inbound.ExternalID,
		DataHora:   iso,
		Status:     "Agendada",
		Tipo:       sr.Kind,
		Curso:      sr.Course,
		Idade:      sr.Age,
		Resumo:     sr.Notes,
		DuracaoMin: cfg.AulaDuracaoMin,
	})
	if err != nil {
		log.Error("agenda: falha ao gravar no Notion", "err", err, "quando", iso)
		// Remarcação que morre no meio é o pior caso da função: a aula antiga
		// já saiu e a nova não entrou, então o cliente acha que tem horário e
		// não tem. Não dá para desarquivar, então isso vira recado humano —
		// alguém precisa ligar para essa família hoje.
		//
		// Com aula anterior, o aviso é o específico (conta o que se perdeu).
		// Sem ela, cai no aviso geral pelo defer — nos dois casos alguém da
		// escola fica sabendo, nunca os dois avisos ao mesmo tempo.
		if temAnterior {
			e.avisaAdminsDeRemarcacaoQuebrada(ctx, conv, inbound, aluno, anterior.Em, iso)
		} else {
			motivoFalha = "o Notion recusou a gravação"
		}
		return false
	}

	log.Info("agenda: aula marcada pelo bot", "aluno", aluno, "quando", iso, "conversa", conv.ID)

	// A aula marcada é o sinal que mais pesa no grau do lead. Quem conversou,
	// ouviu o preço e mesmo assim marcou é outra categoria de interessado.
	if e.deps.Qualificacoes != nil {
		if err := e.deps.Qualificacoes.MarcaAula(ctx, inbound.TenantID, conv.ContactID); err != nil {
			log.Error("qualificacao: falha ao registrar a aula no dossiê", "err", err)
		}
	}

	// A pendência cumpriu o papel dela: existe para o caso de o bot NÃO
	// conseguir marcar. Marcou, então some do painel — deixada aberta, um admin
	// confirmando por lá criaria uma segunda aula no horário abandonado.
	if e.deps.Bookings != nil {
		if err := e.withTenant(ctx, func(tx pgx.Tx) error {
			n, err := e.deps.Bookings.FecharAbertasDaConversa(ctx, tx, inbound.TenantID, conv.ID, "confirmed")
			if err == nil && n > 0 {
				log.Info("agenda: pendências fechadas pelo agendamento automático", "quantas", n)
			}
			return err
		}); err != nil {
			log.Error("agenda: aula marcada mas a pendência continua aberta no painel",
				"err", err, "conversa", conv.ID)
		}
	}

	e.avisaAdminsDoAgendamento(ctx, conv, inbound, aluno, sr, iso)
	// O Notion é o controle; o Google Agenda é o alarme. Falhar aqui não
	// desfaz a aula — ela existe e está avisada por WhatsApp.
	e.poeNoGoogleAgenda(ctx, conv, inbound, pageID, aluno, sr, inicio, dur)
	e.agendaLembretesDoCliente(ctx, conv, inbound, pageID, aluno, inicio)
	return true
}

// avisaAdminsDeAgendamentoFalho avisa quando o bot prometeu e não cumpriu.
//
// O cliente aceitou o horário e leu "está marcado" — as bolhas saem antes de
// a gravação acontecer. Se a gravação falha, existe uma pessoa achando que tem
// aula e uma agenda que não sabe disso. O silêncio é o pior desfecho: ninguém
// descobre até a família aparecer na porta.
func (e *ConversationEngine) avisaAdminsDeAgendamentoFalho(ctx context.Context, conv Conversation, inbound InboundMessage, aluno string, sr *SchedulingRequest, motivo string) {
	if e.deps.Emitter == nil {
		return
	}
	quando := strings.TrimSpace(firstNonEmpty(sr.ProposedDate, sr.ProposedDay) + " " + sr.ProposedTime)
	msg := fmt.Sprintf("⚠️ NÃO consegui marcar a aula de %s (%s), mas JÁ DISSE AO CLIENTE que estava marcada. Motivo: %s. Precisa lançar na mão e confirmar com ele: %s",
		aluno, quando, motivo, inbound.ExternalID)
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
		e.deps.Logger.Error("agenda: falha ao avisar do agendamento que não saiu", "err", err)
	}
}

// avisaAdminsDeRemarcacaoQuebrada grita quando a remarcação morre no meio.
//
// A aula antiga já foi arquivada e a nova não entrou. O Notion não desarquiva
// por API, então não existe desfazer automático: o que dá para fazer é avisar
// quem pode ligar para a família antes que ela apareça num horário que não
// existe mais.
func (e *ConversationEngine) avisaAdminsDeRemarcacaoQuebrada(ctx context.Context, conv Conversation, inbound InboundMessage, aluno string, de time.Time, paraISO string) {
	if e.deps.Emitter == nil {
		return
	}
	msg := fmt.Sprintf("🚨 REMARCAÇÃO INCOMPLETA — %s ficou SEM aula. Tirei a de %s e não consegui criar a de %s. Precisa lançar na mão e avisar o cliente: %s",
		aluno, formatBRDateTime(de.Format(time.RFC3339)), formatBRDateTime(paraISO), inbound.ExternalID)
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
		e.deps.Logger.Error("agenda: falha ao avisar da remarcação quebrada", "err", err)
	}
}

// avisaAdminsDoAgendamento manda o recado para quem opera a escola. É o que
// substitui o "alguém confirmou, então alguém sabe" que existia antes.
func (e *ConversationEngine) avisaAdminsDoAgendamento(ctx context.Context, conv Conversation, inbound InboundMessage, aluno string, sr *SchedulingRequest, iso string) {
	if e.deps.Emitter == nil {
		return
	}
	// O grau vai junto: é a diferença entre "mais uma aula marcada" e "esta
	// família respondeu tudo, ouviu o preço e veio mesmo assim". Sem isso no
	// aviso, a classificação existe no banco e não muda nada na prática.
	grau := ""
	if e.deps.Qualificacoes != nil {
		q := e.deps.Qualificacoes.Get(ctx, inbound.TenantID, conv.ContactID)
		grau = "\nLead: " + q.Grau().Legivel()
		if q.Motivacao != "" {
			grau += "\nMotivo: " + q.Motivacao
		}
	}
	msg := fmt.Sprintf("✅ Aula experimental MARCADA pelo bot: %s%s — %s. Cliente: %s%s",
		aluno, courseSuffix(sr.Course), formatBRDateTime(iso), inbound.ExternalID, grau)
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
func (e *ConversationEngine) poeNoGoogleAgenda(ctx context.Context, conv Conversation, inbound InboundMessage, notionPageID, aluno string, sr *SchedulingRequest, inicio time.Time, dur time.Duration) {
	log := e.deps.Logger
	// Cada desistência daqui fala. Antes eram três `return` calados, e "a aula
	// não apareceu na minha agenda" não tinha uma única linha de log para
	// separar "o bot nem tentou" de "o Google recusou" — a diferença entre
	// configurar uma variável e investigar uma integração.
	if e.deps.GCal == nil || !e.deps.GCal.Enabled() {
		log.Warn("gcal: integração desligada (faltam GOOGLE_CLIENT_ID/SECRET); a aula não vai para agenda nenhuma",
			"aula", notionPageID)
		return
	}
	if e.deps.GCalRepo == nil || notionPageID == "" {
		log.Warn("gcal: sem repositório ou sem página do Notion; não dá para vincular o evento")
		return
	}
	contas, err := e.deps.GCalRepo.Ativas(ctx, e.deps.TenantID)
	if err != nil {
		log.Error("gcal: falha ao listar contas", "err", err)
		return
	}
	if len(contas) == 0 {
		log.Warn("gcal: nenhuma agenda autorizada; ninguém vai ser avisado desta aula",
			"aula", notionPageID)
		return
	}

	// A descrição do evento da ESCOLA é interna: leva o resumo que o bot
	// escreveu para um colega ler ("mãe achou caro, pai não quer pagar"). Ela
	// nunca pode ir para o evento onde o cliente é convidado — o convidado lê a
	// descrição no app dele e no e-mail do convite.
	ev := EventoAula{
		Titulo:    TituloAgendamento(aluno),
		Descricao: descricaoDoEvento(sr),
		Inicio:    inicio,
		Fim:       inicio.Add(dur),
	}

	criados := 0
	for _, c := range contas {
		eventID, err := e.deps.GCal.CriarEvento(ctx, c.RefreshToken, c.CalendarID, ev)
		if err != nil {
			log.Error("gcal: falha ao criar evento", "err", err, "conta", c.Email)
			e.deps.GCalRepo.RegistrarErro(ctx, c.ID, err.Error())
			continue
		}
		criados++
		if err := e.deps.GCalRepo.VincularEvento(ctx, e.deps.TenantID, c.ID, notionPageID, eventID, inicio); err != nil {
			log.Error("gcal: evento criado mas não vinculado", "err", err, "conta", c.Email)
		}
		log.Info("gcal: evento criado", "conta", c.Email, "aluno", aluno, "quando", inicio.Format(time.RFC3339))
	}

	// Nenhum evento em nenhuma agenda é falha de verdade: a aula existe no
	// Notion e ninguém vai ser avisado dela. Vira recado humano, não só log.
	if criados == 0 {
		log.Error("gcal: a aula não entrou em NENHUMA agenda", "aula", notionPageID, "contas", len(contas))
		e.avisaAdminsDeAgendaMuda(ctx, conv, inbound, aluno, inicio)
	}
}

// avisaAdminsDeAgendaMuda conta que a aula ficou só no Notion.
//
// O Notion é o registro; o Google Agenda é o alarme. Sem o alarme, a aula existe
// e ninguém é lembrado dela — e o Notion não avisa ninguém sozinho.
func (e *ConversationEngine) avisaAdminsDeAgendaMuda(ctx context.Context, conv Conversation, inbound InboundMessage, aluno string, inicio time.Time) {
	if e.deps.Emitter == nil {
		return
	}
	msg := fmt.Sprintf("⚠️ A aula de %s (%s) foi marcada no Notion mas NÃO entrou no Google Agenda de ninguém. Vocês não vão receber lembrete dela — vale conferir a autorização das contas.",
		aluno, formatBRDateTime(inicio.Format(time.RFC3339)))
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
		e.deps.Logger.Error("gcal: falha ao avisar que a agenda ficou muda", "err", err)
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
		// Esta tabela é o livro-razão de quais aulas são do bot: sem a linha, a
		// conversa "não tem" aula marcada, o cliente fica sem os três lembretes
		// e a próxima confirmação cria uma SEGUNDA aula em vez de remarcar.
		e.deps.Logger.Error("lembretes: a aula existe mas ficou sem registro; cliente não será lembrado e a remarcação vai duplicar",
			"err", err, "aula", notionPageID, "aluno", aluno)
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
func (e *ConversationEngine) cancelaAulaDoCliente(ctx context.Context, conv Conversation, inbound InboundMessage) {
	log := e.deps.Logger
	if e.deps.Notion == nil || !e.deps.Notion.Enabled() || e.deps.Lembretes == nil {
		return
	}

	// A aula é achada pelos registros DO BOT, não varrendo a agenda.
	//
	// A base da escola não tem campo de WhatsApp, então não dá para procurar
	// pelo telefone. E mesmo que tivesse, procurar assim acharia aulas lançadas
	// à mão — que o bot não pode mexer. Partir do que ele mesmo criou resolve
	// as duas coisas de uma vez.
	aula, ok := e.deps.Lembretes.AulaDaConversa(ctx, inbound.TenantID, string(conv.ID))
	if !ok {
		log.Info("cancelamento: esta conversa não tem aula marcada pelo bot", "de", inbound.ExternalID)
		return
	}

	if err := e.deps.Notion.ArquivarBooking(ctx, aula.PageID); err != nil {
		log.Error("cancelamento: falha ao arquivar", "err", err, "aula", aula.PageID)
		return
	}
	// Erro aqui não pode passar batido: sem cancelar as linhas, o cliente
	// continua recebendo "confirma sua aula de amanhã?" de uma aula que ele
	// acabou de desmarcar.
	if n, err := e.deps.Lembretes.CancelarDaAula(ctx, inbound.TenantID, aula.PageID); err != nil {
		log.Error("cancelamento: aula arquivada mas os lembretes continuam vivos",
			"err", err, "aula", aula.PageID)
	} else if n > 0 {
		log.Info("cancelamento: lembretes cancelados", "quantos", n)
	}
	e.tiraDoGoogleAgenda(ctx, inbound.TenantID, aula.PageID)
	log.Info("cancelamento: horário liberado", "aula", aula.PageID, "era_em", aula.Em.Format(time.RFC3339))
}

// convidaClienteNoEvento põe o cliente no evento que já existe.
//
// Convida em UM evento só, o primeiro. Os dois eventos (Rodrigo e Henrique)
// representam a mesma aula; convidar nos dois mandaria dois convites da mesma
// coisa para a mesma pessoa.
func (e *ConversationEngine) convidaClienteNoEvento(ctx context.Context, notionPageID, email string) {
	log := e.deps.Logger
	if e.deps.GCal == nil || !e.deps.GCal.Enabled() || e.deps.GCalRepo == nil || notionPageID == "" {
		return
	}
	eventos, err := e.deps.GCalRepo.EventosDaAula(ctx, e.deps.TenantID, notionPageID)
	if err != nil {
		log.Error("gcal: falha ao buscar o evento para convidar o cliente", "err", err)
		return
	}
	if len(eventos) == 0 {
		log.Info("gcal: aula sem evento na agenda; não dá para convidar", "aula", notionPageID)
		return
	}
	ev := eventos[0]
	if err := e.deps.GCal.ConvidarNoEvento(ctx, ev.Conta.RefreshToken, ev.Conta.CalendarID, ev.EventID, email, descricaoParaOCliente()); err != nil {
		log.Error("gcal: falha ao convidar o cliente", "err", err, "conta", ev.Conta.Email)
		return
	}
	log.Info("gcal: cliente convidado na agenda", "aula", notionPageID, "conta", ev.Conta.Email)
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

// descricaoParaOCliente é o que o convidado lê no app de agenda dele e no
// e-mail do convite.
//
// Deliberadamente sem o resumo do atendimento. Aquele texto é escrito para um
// colega da escola ler antes da aula — "mãe achou caro", "pai não quer pagar",
// "já tentou outro curso e desistiu" — e é exatamente o tipo de anotação que
// não pode chegar à pessoa de quem se está falando.
func descricaoParaOCliente() string {
	return "Aula experimental na Escola Santos Tech.\n\n" +
		"Av. Nove de Julho, 1992 — Jardim América, Ribeirão Preto.\n" +
		"Temos garagem própria, é só entrar.\n\n" +
		"Qualquer coisa, é só chamar no WhatsApp."
}
