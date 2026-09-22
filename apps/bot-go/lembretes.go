package main

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Lembretes da aula experimental, para o cliente.
//
// Três momentos, com propósitos diferentes — e é por isso que as mensagens são
// diferentes, não só reescritas:
//
//	véspera  — CONFIRMA. Ainda dá tempo de remarcar e liberar o horário para
//	           outra pessoa se a resposta for não.
//	4 horas  — LEMBRA. O dia já começou; a pessoa está organizando a tarde.
//	1 hora   — RECEBE. Diz onde chegar e com quem falar, que é o que tira o
//	           desconforto de entrar num lugar novo.
//
// Mandar a mesma frase três vezes ensina o cliente a ignorar a quarta.

type TipoLembrete string

const (
	LembreteVespera     TipoLembrete = "vespera"
	LembreteQuatroHoras TipoLembrete = "quatro_horas"
	LembreteUmaHora     TipoLembrete = "uma_hora"
)

// antecedencia de cada lembrete em relação à aula.
var antecedenciaLembrete = map[TipoLembrete]time.Duration{
	LembreteVespera:     24 * time.Hour,
	LembreteQuatroHoras: 4 * time.Hour,
	LembreteUmaHora:     1 * time.Hour,
}

// textosLembrete — variantes por momento. O sorteio evita que duas famílias
// recebam exatamente a mesma frase no mesmo dia, o que denuncia automação.
var textosLembrete = map[TipoLembrete][]string{
	LembreteVespera: {
		"Olá! Estou passando pra confirmar a nossa aula experimental, que está agendada para amanhã. Está tudo certo pra você?",
		"Oi! Passando pra confirmar: amanhã temos a aula experimental marcada. Posso confirmar sua presença?",
		"Olá! Só confirmando a aula experimental de amanhã. Continua tudo certo aí?",
	},
	LembreteQuatroHoras: {
		"Oi! Só passando pra lembrar que daqui a quatro horinhas está marcado o nosso compromisso da aula experimental.",
		"Olá! Lembrete rápido: a aula experimental é hoje, daqui a pouco mais de quatro horas.",
		"Oi! Passando pra lembrar da nossa aula experimental, que é hoje daqui a quatro horas.",
	},
	LembreteUmaHora: {
		"Olá! O professor já está se organizando pra te receber. Quando chegar, pode procurar pela recepcionista Verônica.",
		"Oi! Está quase na hora. Quando chegar aqui, é só falar com a Verônica na recepção que ela te encaminha.",
		"Olá! Já estamos te esperando. Ao chegar, procure pela Verônica na recepção.",
	},
}

// TextoDoLembrete sorteia uma variante do momento pedido.
func TextoDoLembrete(kind TipoLembrete) string {
	v := textosLembrete[kind]
	if len(v) == 0 {
		return ""
	}
	return v[rand.Intn(len(v))]
}

// ── repositório ──────────────────────────────────────────────────────────────

type LembreteRepo struct{ pool *pgxpool.Pool }

func NewLembreteRepo(pool *pgxpool.Pool) *LembreteRepo { return &LembreteRepo{pool: pool} }

// LembretePendente — o que o worker precisa para mandar a mensagem.
type LembretePendente struct {
	ID             string
	NotionPageID   string
	ConversationID string
	ClientPhone    string
	Channel        string
	Aluno          string
	Kind           TipoLembrete
	AulaEm         time.Time
}

// Agendar cria os três lembretes de uma aula.
//
// Os que já nasceriam vencidos são pulados, não gravados como atrasados: aula
// marcada para daqui a duas horas não tem por que disparar um "confirma pra
// amanhã" no mesmo minuto.
func (r *LembreteRepo) Agendar(ctx context.Context, tenantID TenantID, notionPageID, convID, phone, channel, aluno string, aulaEm time.Time, agora time.Time) (int, error) {
	if notionPageID == "" || phone == "" {
		return 0, nil
	}
	criados := 0
	for _, kind := range []TipoLembrete{LembreteVespera, LembreteQuatroHoras, LembreteUmaHora} {
		enviarEm := aulaEm.Add(-antecedenciaLembrete[kind])
		if !enviarEm.After(agora) {
			continue
		}
		var convArg any
		if convID != "" {
			convArg = convID
		}
		canal := channel
		if canal == "" {
			canal = "whatsapp"
		}
		_, err := r.pool.Exec(ctx, `
			INSERT INTO booking_reminder
			  (tenant_id, notion_page_id, conversation_id, client_phone, channel, aluno, kind, aula_em, enviar_em)
			VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (tenant_id, notion_page_id, kind) DO UPDATE SET
			  aula_em   = EXCLUDED.aula_em,
			  enviar_em = EXCLUDED.enviar_em,
			  status    = CASE WHEN booking_reminder.status = 'enviado'
			                   THEN booking_reminder.status ELSE 'pendente' END
		`, tenantID, notionPageID, convArg, phone, canal, aluno, string(kind), aulaEm, enviarEm)
		if err != nil {
			return criados, fmt.Errorf("LembreteRepo.Agendar (%s): %w", kind, err)
		}
		criados++
	}
	return criados, nil
}

// Vencidos devolve e RESERVA os lembretes prontos para sair.
//
// O UPDATE ... RETURNING com FOR UPDATE SKIP LOCKED é o que permite mais de uma
// réplica do worker sem mandar o lembrete duas vezes: quem pega, marca; quem
// chegou depois pula a linha travada.
func (r *LembreteRepo) Vencidos(ctx context.Context, tenantID TenantID, limite int) ([]LembretePendente, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE booking_reminder SET status = 'enviando', tentativas = tentativas + 1
		WHERE id IN (
		  SELECT id FROM booking_reminder
		  WHERE tenant_id = $1 AND status = 'pendente' AND enviar_em <= now()
		  ORDER BY enviar_em
		  LIMIT $2
		  FOR UPDATE SKIP LOCKED
		)
		RETURNING id::text, notion_page_id, coalesce(conversation_id::text, ''),
		          client_phone, channel, aluno, kind, aula_em
	`, tenantID, limite)
	if err != nil {
		return nil, fmt.Errorf("LembreteRepo.Vencidos: %w", err)
	}
	defer rows.Close()
	var out []LembretePendente
	for rows.Next() {
		var l LembretePendente
		var kind string
		if err := rows.Scan(&l.ID, &l.NotionPageID, &l.ConversationID, &l.ClientPhone,
			&l.Channel, &l.Aluno, &kind, &l.AulaEm); err != nil {
			return nil, fmt.Errorf("LembreteRepo.Vencidos scan: %w", err)
		}
		l.Kind = TipoLembrete(kind)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *LembreteRepo) MarcarEnviado(ctx context.Context, id string) {
	_, _ = r.pool.Exec(ctx, `
		UPDATE booking_reminder SET status = 'enviado', enviado_em = now(), last_error = NULL
		WHERE id = $1::uuid`, id)
}

// MarcarFalha devolve para a fila até três tentativas; depois desiste.
//
// Desistir importa: um telefone inválido não pode fazer o worker tentar para
// sempre e esconder os lembretes que funcionariam.
func (r *LembreteRepo) MarcarFalha(ctx context.Context, id, msg string) {
	_, _ = r.pool.Exec(ctx, `
		UPDATE booking_reminder
		   SET status = CASE WHEN tentativas >= 3 THEN 'falhou' ELSE 'pendente' END,
		       last_error = $2
		 WHERE id = $1::uuid`, id, msg)
}

// CancelarDaAula apaga os lembretes pendentes de uma aula — usado quando a aula
// é cancelada ou remarcada.
func (r *LembreteRepo) CancelarDaAula(ctx context.Context, tenantID TenantID, notionPageID string) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE booking_reminder SET status = 'cancelado'
		WHERE tenant_id = $1 AND notion_page_id = $2 AND status IN ('pendente', 'enviando')
	`, tenantID, notionPageID)
	if err != nil {
		return 0, fmt.Errorf("LembreteRepo.CancelarDaAula: %w", err)
	}
	return tag.RowsAffected(), nil
}

// MensagemDoLembrete monta o texto final.
//
// O nome do aluno entra quando existe — "a aula do Guilherme" soa como alguém
// que sabe com quem está falando, "a aula experimental" soa como sistema.
func MensagemDoLembrete(l LembretePendente) string {
	base := TextoDoLembrete(l.Kind)
	if base == "" {
		return ""
	}
	aluno := strings.TrimSpace(l.Aluno)
	// O título vem com o marcador ("Aula experimental — Guilherme"); aqui
	// interessa só o nome.
	if i := strings.Index(aluno, "—"); i >= 0 {
		aluno = strings.TrimSpace(aluno[i+len("—"):])
	}
	if aluno == "" || l.Kind == LembreteUmaHora {
		return base
	}
	return strings.Replace(base, "a aula experimental", "a aula experimental do "+aluno, 1)
}

// AulaDaConversa devolve a próxima aula que O BOT marcou nesta conversa.
//
// Substitui a busca por telefone na agenda: a base real da escola não tem campo
// de WhatsApp, e mesmo que tivesse, procurar por telefone acharia também aulas
// lançadas à mão — que o bot não pode mexer. Aqui só aparece o que ele criou.
func (r *LembreteRepo) AulaDaConversa(ctx context.Context, tenantID TenantID, convID string) (notionPageID string, aulaEm time.Time, ok bool) {
	if convID == "" {
		return "", time.Time{}, false
	}
	err := r.pool.QueryRow(ctx, `
		SELECT notion_page_id, aula_em
		FROM booking_reminder
		WHERE tenant_id = $1 AND conversation_id = $2::uuid
		  AND aula_em > now()
		  AND status IN ('pendente', 'enviando', 'enviado')
		ORDER BY aula_em
		LIMIT 1
	`, tenantID, convID).Scan(&notionPageID, &aulaEm)
	if err != nil {
		return "", time.Time{}, false
	}
	return notionPageID, aulaEm, notionPageID != ""
}
