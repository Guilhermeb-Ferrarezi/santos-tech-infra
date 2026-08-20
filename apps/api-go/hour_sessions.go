package main

// Controle de horas de clientes (lan house/escola). Tempo decorrido nunca é
// guardado como contador acumulado — é sempre recalculado a partir dos
// eventos em hour_session_events (start/pause/resume/end), então uma sessão
// nunca dessincroniza mesmo se um passo intermediário falhar.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type HourClient struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Phone          *string   `json:"phone"`
	BalanceMinutes int       `json:"balanceMinutes"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// HourSession é a view completa (admin) ou pública, dependendo do handler que
// a monta — ClientName/BalanceMinutes vêm de join com hour_clients.
type HourSession struct {
	ID               string     `json:"id"`
	ClientID         string     `json:"clientId"`
	ClientName       string     `json:"clientName"`
	Status           string     `json:"status"`
	PauseRequestedAt *time.Time `json:"pauseRequestedAt"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	ElapsedSeconds   int64      `json:"elapsedSeconds"`
	BalanceMinutes   int        `json:"balanceMinutes"`
	// ScheduledEndAt: fim agendado opcional (duração ou horário fixo, o front
	// resolve os dois pra um timestamp absoluto antes de mandar). NULL =
	// sessão de duração livre, "até o admin encerrar" (comportamento padrão).
	// Ver autoEndIfDue — é isso que efetivamente encerra sozinha ao passar.
	ScheduledEndAt *time.Time `json:"scheduledEndAt"`
	// ScheduledStartAt: início agendado opcional. Não nulo = sessão nasce
	// status "scheduled" (sem evento 'start', elapsed=0, cliente não consegue
	// usar ainda) e vira "active" sozinha ao chegar nesse horário — ver
	// autoStartIfDue. NULL = começa na hora (comportamento padrão).
	ScheduledStartAt *time.Time `json:"scheduledStartAt"`
}

type hourSessionEvent struct {
	EventType string
	CreatedAt time.Time
	// DeltaSeconds só é usado por 'adjust' (positivo soma, negativo desconta).
	DeltaSeconds int64
}

// HourSessionEvent é uma linha do histórico da sessão, como o admin vê:
// start/pause/resume/end e os ajustes manuais, com quem fez cada um.
type HourSessionEvent struct {
	ID           string    `json:"id"`
	EventType    string    `json:"eventType"`
	DeltaSeconds int64     `json:"deltaSeconds"`
	Note         *string   `json:"note"`
	ActorName    *string   `json:"actorName"`
	CreatedAt    time.Time `json:"createdAt"`
}

var errHourClientNotFound = appErr(http.StatusNotFound, "HOUR_CLIENT_NOT_FOUND", "Cliente não encontrado")
var errHourSessionNotFound = appErr(http.StatusNotFound, "HOUR_SESSION_NOT_FOUND", "Sessão não encontrada")

// ── clientes ─────────────────────────────────────────────────────────────────

func (s *Server) insertHourClient(ctx context.Context, name string, phone *string) (*HourClient, error) {
	var c HourClient
	err := s.db.QueryRow(ctx, `
		INSERT INTO hour_clients (name, phone) VALUES ($1, $2)
		RETURNING id::text, name, phone, balance_minutes, created_at, updated_at`,
		name, phone).
		Scan(&c.ID, &c.Name, &c.Phone, &c.BalanceMinutes, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Server) listHourClients(ctx context.Context) ([]HourClient, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, name, phone, balance_minutes, created_at, updated_at
		FROM hour_clients ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HourClient{}
	for rows.Next() {
		var c HourClient
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.BalanceMinutes, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Server) getHourClient(ctx context.Context, id string) (*HourClient, error) {
	var c HourClient
	err := s.db.QueryRow(ctx, `
		SELECT id::text, name, phone, balance_minutes, created_at, updated_at
		FROM hour_clients WHERE id = $1::uuid`, id).
		Scan(&c.ID, &c.Name, &c.Phone, &c.BalanceMinutes, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// addHourPurchase registra a compra (auditoria) e credita o saldo do cliente
// na mesma transação.
func (s *Server) addHourPurchase(ctx context.Context, clientID string, minutesAdded int, note *string, createdBy int64) (*HourClient, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO hour_purchases (client_id, minutes_added, note, created_by)
		VALUES ($1::uuid, $2, $3, $4)`,
		clientID, minutesAdded, note, createdBy); err != nil {
		return nil, err
	}
	var c HourClient
	err = tx.QueryRow(ctx, `
		UPDATE hour_clients SET balance_minutes = balance_minutes + $2, updated_at = now()
		WHERE id = $1::uuid
		RETURNING id::text, name, phone, balance_minutes, created_at, updated_at`,
		clientID, minutesAdded).
		Scan(&c.ID, &c.Name, &c.Phone, &c.BalanceMinutes, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errHourClientNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &c, nil
}

// ── sessões ──────────────────────────────────────────────────────────────────

const hourSessionCols = `s.id::text, s.client_id::text, c.name, s.status, s.pause_requested_at,
	s.created_at, s.updated_at, c.balance_minutes, s.scheduled_end_at, s.scheduled_start_at`

func scanHourSession(row pgx.Row) (*HourSession, error) {
	var h HourSession
	err := row.Scan(&h.ID, &h.ClientID, &h.ClientName, &h.Status, &h.PauseRequestedAt,
		&h.CreatedAt, &h.UpdatedAt, &h.BalanceMinutes, &h.ScheduledEndAt, &h.ScheduledStartAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// shortCodeTTL é a validade do código curto de pareamento (6 dígitos). 15min
// era curto demais pro uso real (admin pode preparar sessões bem antes do
// cliente sentar no PC) — 24h cobre um dia de operação. Ainda é uso único
// (zerado no primeiro pareamento) e o rate limit de POST /pair-by-code é
// 15/min por IP: força-bruta nas 1M combinações levaria ~46 dias contínuos
// de uma IP só, então o risco não escala muito ao alongar a janela.
const shortCodeTTL = 24 * time.Hour

// releaseStaleShortCodes devolve ao espaço UNIQUE os códigos curtos que já não
// servem pra nada: expirados, ou de sessões encerradas. Sem isto todo código
// já emitido ocupava um dos 1M valores pra sempre — com o tempo, colisão no
// INSERT viraria 500 na cara do admin. Roda antes de sortear um código novo;
// é um UPDATE indexado (idx_hour_sessions_short_code_stale) e a rota é
// admin-only, então o custo é irrelevante.
func (s *Server) releaseStaleShortCodes(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `
		UPDATE hour_sessions SET short_code = NULL, short_code_expires_at = NULL
		WHERE short_code IS NOT NULL AND (short_code_expires_at <= now() OR status = 'ended')`)
	return err
}

// startHourSessionAttempts: o código curto é sorteado (randomDigits) e tem
// UNIQUE no banco, então uma colisão é possível. Sem retry ela virava 500;
// com o espaço já liberado por releaseStaleShortCodes, 3 tentativas cobrem
// qualquer cenário realista.
const startHourSessionAttempts = 3

// startHourSession cria a sessão numa transação e devolve o token em texto
// puro e o código curto (só existem neste retorno — o banco guarda o hash do
// token e o próprio código, mas o código é zerado no primeiro pareamento,
// ver pairHourSessionByCode).
//
// scheduledEndAt é opcional (nil = duração livre, "até o admin encerrar") —
// o front resolve duração/horário fixo pra um timestamp absoluto antes de
// mandar. scheduledStartAt também é opcional: nil cria a sessão já "active"
// com evento 'start' agora, igual sempre foi; um horário FUTURO cria como
// "scheduled" (sem evento 'start' ainda — ver autoStartIfDue, que é quem de
// fato inicia sozinha ao chegar a hora); um horário PASSADO cria já "active"
// mas com o evento 'start' backdatado pra esse horário — início retroativo,
// pro admin que esqueceu de abrir a sessão e o cliente já tinha começado.
func (s *Server) startHourSession(ctx context.Context, clientID string, createdBy int64, scheduledEndAt, scheduledStartAt *time.Time) (*HourSession, string, string, error) {
	if err := s.releaseStaleShortCodes(ctx); err != nil {
		return nil, "", "", err
	}
	var lastErr error
	for i := 0; i < startHourSessionAttempts; i++ {
		h, token, shortCode, err := s.startHourSessionOnce(ctx, clientID, createdBy, scheduledEndAt, scheduledStartAt)
		if err == nil {
			return h, token, shortCode, nil
		}
		if !isUniqueViolation(err) {
			return nil, "", "", err
		}
		lastErr = err // colisão de short_code: sorteia outro
	}
	return nil, "", "", lastErr
}

func (s *Server) startHourSessionOnce(ctx context.Context, clientID string, createdBy int64, scheduledEndAt, scheduledStartAt *time.Time) (*HourSession, string, string, error) {
	token := randomToken(32)
	tokenHash := sha256Hex(token)
	shortCode := randomDigits(6)
	startsImmediately := scheduledStartAt == nil || !scheduledStartAt.After(time.Now())
	initialStatus := "active"
	if !startsImmediately {
		initialStatus = "scheduled"
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, "", "", err
	}
	defer tx.Rollback(ctx)
	var sessionID string
	err = tx.QueryRow(ctx, `
		INSERT INTO hour_sessions (client_id, status, token_hash, short_code, short_code_expires_at, created_by, scheduled_end_at, scheduled_start_at)
		VALUES ($1::uuid, $2, $3, $4, now() + make_interval(mins => $5), $6, $7, $8)
		RETURNING id::text`,
		clientID, initialStatus, tokenHash, shortCode, int(shortCodeTTL.Minutes()), createdBy, scheduledEndAt, scheduledStartAt).Scan(&sessionID)
	if err != nil {
		return nil, "", "", err
	}
	if startsImmediately {
		// scheduledStartAt no passado = início retroativo (admin esqueceu de
		// abrir a sessão e o cliente já tinha começado): o evento 'start' nasce
		// com esse horário passado em vez de now(), então o tempo decorrido já
		// conta desde lá — não precisa de ajuste manual depois.
		startEventAt := time.Now()
		if scheduledStartAt != nil {
			startEventAt = *scheduledStartAt
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO hour_session_events (session_id, event_type, actor_user_id, created_at)
			VALUES ($1::uuid, 'start', $2, $3)`,
			sessionID, createdBy, startEventAt); err != nil {
			return nil, "", "", err
		}
	}
	h, err := scanHourSession(tx.QueryRow(ctx, `
		SELECT `+hourSessionCols+` FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
		WHERE s.id = $1::uuid`, sessionID))
	if err != nil {
		return nil, "", "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", "", err
	}
	h.ElapsedSeconds = 0
	if startsImmediately && scheduledStartAt != nil {
		// Início retroativo: o evento 'start' já nasceu no passado, então o
		// tempo decorrido não começa em zero.
		h.ElapsedSeconds = computeElapsedSeconds([]hourSessionEvent{{EventType: "start", CreatedAt: *scheduledStartAt}}, time.Now())
	}
	return h, token, shortCode, nil
}

// reissueHourSessionToken gera um novo token pro link público de uma sessão
// já existente (ex.: admin perdeu o link original, ou a sessão foi iniciada
// antes de existir cache local pro link). O token antigo para de funcionar —
// só o hash mais recente fica válido.
func (s *Server) reissueHourSessionToken(ctx context.Context, id string) (string, error) {
	token := randomToken(32)
	tokenHash := sha256Hex(token)
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_sessions SET token_hash = $2, updated_at = now()
		WHERE id = $1::uuid AND status != 'ended'`,
		id, tokenHash)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", errHourSessionNotFound
	}
	return token, nil
}

var errHourSessionCodeInvalid = appErr(http.StatusNotFound, "HOUR_SESSION_CODE_INVALID", "Código inválido ou expirado")

// pairHourSessionByCode troca um código curto ainda válido por um token novo
// — reemite (como reissueHourSessionToken) em vez de devolver o token
// original, que nunca fica guardado em texto puro depois da criação. O
// código é zerado na mesma UPDATE: uso único, mesmo se ainda não tivesse
// expirado.
func (s *Server) pairHourSessionByCode(ctx context.Context, code string) (string, error) {
	token := randomToken(32)
	tokenHash := sha256Hex(token)
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_sessions SET token_hash = $2, short_code = NULL, short_code_expires_at = NULL, updated_at = now()
		WHERE short_code = $1 AND status != 'ended' AND short_code_expires_at > now()`,
		code, tokenHash)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", errHourSessionCodeInvalid
	}
	return token, nil
}

// listActiveHourSessions traz as sessões ainda não encerradas (painel "ao
// vivo" do admin), com o tempo decorrido já calculado.
func (s *Server) listActiveHourSessions(ctx context.Context) ([]HourSession, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+hourSessionCols+`
		FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
		WHERE s.status != 'ended'
		ORDER BY s.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HourSession{}
	for rows.Next() {
		h, err := scanHourSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range out {
		if err := s.autoStartIfDue(ctx, &out[i]); err != nil {
			return nil, err
		}
		if err := s.autoEndIfDue(ctx, &out[i]); err != nil {
			return nil, err
		}
		elapsed, err := s.hourSessionElapsedSeconds(ctx, out[i].ID, now)
		if err != nil {
			return nil, err
		}
		out[i].ElapsedSeconds = elapsed
	}
	return out, nil
}

func (s *Server) getHourSession(ctx context.Context, id string) (*HourSession, error) {
	h, err := scanHourSession(s.db.QueryRow(ctx, `
		SELECT `+hourSessionCols+` FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
		WHERE s.id = $1::uuid`, id))
	if err != nil || h == nil {
		return h, err
	}
	if err := s.autoStartIfDue(ctx, h); err != nil {
		return nil, err
	}
	if err := s.autoEndIfDue(ctx, h); err != nil {
		return nil, err
	}
	elapsed, err := s.hourSessionElapsedSeconds(ctx, h.ID, time.Now())
	if err != nil {
		return nil, err
	}
	h.ElapsedSeconds = elapsed
	return h, nil
}

// getHourSessionByTokenHash é a leitura usada pela rota pública (sem auth) —
// mesmo shape de getHourSession, buscando por token em vez de id.
func (s *Server) getHourSessionByTokenHash(ctx context.Context, tokenHash string) (*HourSession, error) {
	h, err := scanHourSession(s.db.QueryRow(ctx, `
		SELECT `+hourSessionCols+` FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
		WHERE s.token_hash = $1`, tokenHash))
	if err != nil || h == nil {
		return h, err
	}
	if err := s.autoStartIfDue(ctx, h); err != nil {
		return nil, err
	}
	if err := s.autoEndIfDue(ctx, h); err != nil {
		return nil, err
	}
	elapsed, err := s.hourSessionElapsedSeconds(ctx, h.ID, time.Now())
	if err != nil {
		return nil, err
	}
	h.ElapsedSeconds = elapsed
	return h, nil
}

// autoStartIfDue inicia a sessão sozinha (grava o evento 'start' que faltava
// e vira "active") quando o início agendado já chegou — mesmo princípio do
// autoEndIfDue logo abaixo: chamado em todo caminho de leitura de sessão
// "scheduled", sem worker/cron à parte. Roda ANTES de autoEndIfDue em cada
// caller: uma sessão com início E fim já passados (agendou uma janela curta
// e ninguém abriu o link nesse meio tempo) precisa iniciar e encerrar na
// mesma leitura, nessa ordem.
func (s *Server) autoStartIfDue(ctx context.Context, h *HourSession) error {
	if h == nil || h.Status != "scheduled" || h.ScheduledStartAt == nil || h.ScheduledStartAt.After(time.Now()) {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var createdBy int64
	var current string
	if err := tx.QueryRow(ctx, `SELECT created_by, status FROM hour_sessions WHERE id = $1::uuid`, h.ID).
		Scan(&createdBy, &current); err != nil {
		return err
	}
	if current != "scheduled" {
		// Outra leitura concorrente já iniciou entre o scan e aqui — recarrega
		// o estado atual em vez de tentar iniciar de novo (corrida inofensiva).
		fresh, ferr := scanHourSession(s.db.QueryRow(ctx, `
			SELECT `+hourSessionCols+` FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
			WHERE s.id = $1::uuid`, h.ID))
		if ferr != nil {
			return ferr
		}
		*h = *fresh
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE hour_sessions SET status = 'active', updated_at = now() WHERE id = $1::uuid`, h.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO hour_session_events (session_id, event_type, actor_user_id)
		VALUES ($1::uuid, 'start', $2)`,
		h.ID, createdBy); err != nil {
		return err
	}
	fresh, err := scanHourSession(tx.QueryRow(ctx, `
		SELECT `+hourSessionCols+` FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
		WHERE s.id = $1::uuid`, h.ID))
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	*h = *fresh
	return nil
}

// autoEndIfDue encerra a sessão sozinha (mesma lógica de endHourSession,
// autor = quem criou a sessão) quando o fim agendado já passou — chamado em
// TODO caminho de leitura de sessão ativa (painel admin, poll do app do PC,
// heartbeat), então o atraso real é no máximo o intervalo entre polls, não
// depende de nenhum worker/cron rodando à parte. Se outra leitura concorrente
// já encerrou entre o scan e aqui (HOUR_SESSION_BAD_STATE), só recarrega o
// estado atual — não é erro de verdade, é corrida inofensiva.
func (s *Server) autoEndIfDue(ctx context.Context, h *HourSession) error {
	if h == nil || h.Status != "active" || h.ScheduledEndAt == nil || h.ScheduledEndAt.After(time.Now()) {
		return nil
	}
	var createdBy int64
	if err := s.db.QueryRow(ctx, `SELECT created_by FROM hour_sessions WHERE id = $1::uuid`, h.ID).Scan(&createdBy); err != nil {
		return err
	}
	ended, err := s.endHourSession(ctx, h.ID, createdBy)
	if err != nil {
		if ae, ok := err.(*AppError); ok && ae.Code == "HOUR_SESSION_BAD_STATE" {
			fresh, ferr := scanHourSession(s.db.QueryRow(ctx, `
				SELECT `+hourSessionCols+` FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
				WHERE s.id = $1::uuid`, h.ID))
			if ferr != nil {
				return ferr
			}
			*h = *fresh
			return nil
		}
		return err
	}
	*h = *ended
	return nil
}

// hourSessionElapsedSeconds soma os intervalos "rodando" (start/resume até o
// próximo pause/end, ou até `now` se ainda não fechou) a partir do histórico
// de eventos — nunca de um contador guardado.
func (s *Server) hourSessionElapsedSeconds(ctx context.Context, sessionID string, now time.Time) (int64, error) {
	rows, err := s.db.Query(ctx, `
		SELECT event_type, created_at, delta_seconds FROM hour_session_events
		WHERE session_id = $1::uuid ORDER BY created_at`, sessionID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	events := []hourSessionEvent{}
	for rows.Next() {
		var e hourSessionEvent
		if err := rows.Scan(&e.EventType, &e.CreatedAt, &e.DeltaSeconds); err != nil {
			return 0, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return computeElapsedSeconds(events, now), nil
}

func computeElapsedSeconds(events []hourSessionEvent, now time.Time) int64 {
	var total time.Duration
	running := false
	var since time.Time
	for _, e := range events {
		switch e.EventType {
		case "start", "resume":
			running = true
			since = e.CreatedAt
		case "pause", "end":
			if running {
				total += e.CreatedAt.Sub(since)
				running = false
			}
		case "adjust":
			// Correção manual do admin. Entra na mesma conta dos intervalos
			// para que o total continue sendo derivado do histórico inteiro —
			// nunca um número guardado à parte, que dessincroniza.
			total += time.Duration(e.DeltaSeconds) * time.Second
		}
	}
	if running {
		total += now.Sub(since)
	}
	if total < 0 {
		// Ajuste negativo maior que o tempo corrido: o cliente não fica
		// devendo tempo pra sessão, o piso é zero.
		return 0
	}
	return int64(total.Seconds())
}

// maxAdjustSeconds limita um ajuste isolado a 24h pra cada lado. Não é
// desconfiança do admin: é rede de proteção contra dígito a mais numa correção
// digitada na mão, que sairia do saldo do cliente no encerramento.
const maxAdjustSeconds = 24 * 60 * 60

var errAdjustTooLarge = appErr(http.StatusBadRequest, "ADJUST_TOO_LARGE",
	"Ajuste deve estar entre -24h e +24h")

// adjustHourSession registra uma correção manual de tempo como EVENTO.
//
// Não mexe num total acumulado (não existe): o tempo continua derivado do
// histórico, e o ajuste fica lá com autor, horário e motivo. Assim "por que
// essa sessão tem 3h se o cliente ficou 2h?" tem resposta na própria tela, em
// vez de um número que mudou sem explicação.
func (s *Server) adjustHourSession(ctx context.Context, id string, actorID int64, deltaSeconds int64, note *string) (*HourSession, error) {
	if deltaSeconds == 0 || deltaSeconds > maxAdjustSeconds || deltaSeconds < -maxAdjustSeconds {
		return nil, errAdjustTooLarge
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO hour_session_events (session_id, event_type, actor_user_id, delta_seconds, note)
		SELECT $1::uuid, 'adjust', $2, $3, $4
		WHERE EXISTS (SELECT 1 FROM hour_sessions WHERE id = $1::uuid)`,
		id, actorID, deltaSeconds, note)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, errHourSessionNotFound
	}
	return s.getHourSession(ctx, id)
}

// listHourSessionEvents devolve o histórico da sessão em ordem cronológica —
// é o que responde "quem pausou, quando, e por quanto tempo ficou parada".
func (s *Server) listHourSessionEvents(ctx context.Context, sessionID string) ([]HourSessionEvent, error) {
	rows, err := s.db.Query(ctx, `
		SELECT e.id::text, e.event_type, e.delta_seconds, e.note, u.name, e.created_at
		FROM hour_session_events e
		LEFT JOIN users u ON u.id = e.actor_user_id
		WHERE e.session_id = $1::uuid
		ORDER BY e.created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HourSessionEvent{}
	for rows.Next() {
		var e HourSessionEvent
		if err := rows.Scan(&e.ID, &e.EventType, &e.DeltaSeconds, &e.Note, &e.ActorName, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// transitionHourSession aplica pause/resume: valida a troca de estado, grava
// o evento e atualiza o status — tudo numa transação.
func (s *Server) transitionHourSession(ctx context.Context, id string, actorID int64, from, to, eventType string) (*HourSession, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var current string
	err = tx.QueryRow(ctx, `SELECT status FROM hour_sessions WHERE id = $1::uuid`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errHourSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if current != from {
		return nil, appErr(http.StatusConflict, "HOUR_SESSION_BAD_STATE",
			"Sessão não está em estado "+from+" (está "+current+")")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE hour_sessions SET status = $2, updated_at = now() WHERE id = $1::uuid`,
		id, to); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO hour_session_events (session_id, event_type, actor_user_id)
		VALUES ($1::uuid, $2, $3)`,
		id, eventType, actorID); err != nil {
		return nil, err
	}
	h, err := scanHourSession(tx.QueryRow(ctx, `
		SELECT `+hourSessionCols+` FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
		WHERE s.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	elapsed, err := s.hourSessionElapsedSeconds(ctx, id, time.Now())
	if err != nil {
		return nil, err
	}
	h.ElapsedSeconds = elapsed
	return h, nil
}

// endHourSession encerra a sessão e debita o tempo usado do saldo do cliente
// — cálculo do decorrido, evento "end", status e débito de saldo na mesma
// transação. Saldo nunca fica negativo (GREATEST trava em 0): se a sessão
// passou do saldo disponível, o excedente não vira dívida — só zera.
func (s *Server) endHourSession(ctx context.Context, id string, actorID int64) (*HourSession, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var clientID, status string
	err = tx.QueryRow(ctx, `SELECT client_id::text, status FROM hour_sessions WHERE id = $1::uuid`, id).
		Scan(&clientID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errHourSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if status == "ended" {
		return nil, appErr(http.StatusConflict, "HOUR_SESSION_BAD_STATE", "Sessão já foi encerrada")
	}
	elapsed, err := s.hourSessionElapsedSeconds(ctx, id, time.Now())
	if err != nil {
		return nil, err
	}
	elapsedMinutes := int(elapsed / 60)
	// short_code volta pro espaço UNIQUE junto com o encerramento: sessão
	// encerrada não pode mais ser pareada, então guardar o código só gastaria
	// um dos 1M valores pra sempre.
	if _, err := tx.Exec(ctx, `
		UPDATE hour_sessions
		SET status = 'ended', short_code = NULL, short_code_expires_at = NULL, updated_at = now()
		WHERE id = $1::uuid`,
		id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO hour_session_events (session_id, event_type, actor_user_id)
		VALUES ($1::uuid, 'end', $2)`,
		id, actorID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE hour_clients SET balance_minutes = GREATEST(balance_minutes - $2, 0), updated_at = now()
		WHERE id = $1::uuid`,
		clientID, elapsedMinutes); err != nil {
		return nil, err
	}
	h, err := scanHourSession(tx.QueryRow(ctx, `
		SELECT `+hourSessionCols+` FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
		WHERE s.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	h.ElapsedSeconds = elapsed
	return h, nil
}

// requestHourSessionPause marca o pedido do cliente (rota pública) — só grava
// se ainda não havia pedido pendente; quem decide pausar de fato é o admin.
func (s *Server) requestHourSessionPause(ctx context.Context, tokenHash string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_sessions SET pause_requested_at = now(), updated_at = now()
		WHERE token_hash = $1 AND status = 'active' AND pause_requested_at IS NULL`,
		tokenHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return appErr(http.StatusConflict, "HOUR_SESSION_PAUSE_NOT_APPLICABLE",
			"Sessão não está ativa ou já tem pedido de pausa pendente")
	}
	return nil
}

// denyHourSessionPauseRequest limpa o pedido sem pausar (admin recusa).
func (s *Server) denyHourSessionPauseRequest(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_sessions SET pause_requested_at = NULL, updated_at = now()
		WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errHourSessionNotFound
	}
	return nil
}
