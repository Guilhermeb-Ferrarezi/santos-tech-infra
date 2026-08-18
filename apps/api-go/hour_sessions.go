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
}

type hourSessionEvent struct {
	EventType string
	CreatedAt time.Time
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
	s.created_at, s.updated_at, c.balance_minutes`

func scanHourSession(row pgx.Row) (*HourSession, error) {
	var h HourSession
	err := row.Scan(&h.ID, &h.ClientID, &h.ClientName, &h.Status, &h.PauseRequestedAt,
		&h.CreatedAt, &h.UpdatedAt, &h.BalanceMinutes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// startHourSession cria a sessão + evento "start" numa transação e devolve o
// token em texto puro (só existe neste retorno — o banco guarda o hash).
func (s *Server) startHourSession(ctx context.Context, clientID string, createdBy int64) (*HourSession, string, error) {
	token := randomToken(32)
	tokenHash := sha256Hex(token)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)
	var sessionID string
	err = tx.QueryRow(ctx, `
		INSERT INTO hour_sessions (client_id, status, token_hash, created_by)
		VALUES ($1::uuid, 'active', $2, $3)
		RETURNING id::text`,
		clientID, tokenHash, createdBy).Scan(&sessionID)
	if err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO hour_session_events (session_id, event_type, actor_user_id)
		VALUES ($1::uuid, 'start', $2)`,
		sessionID, createdBy); err != nil {
		return nil, "", err
	}
	h, err := scanHourSession(tx.QueryRow(ctx, `
		SELECT `+hourSessionCols+` FROM hour_sessions s JOIN hour_clients c ON c.id = s.client_id
		WHERE s.id = $1::uuid`, sessionID))
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	h.ElapsedSeconds = 0
	return h, token, nil
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
	elapsed, err := s.hourSessionElapsedSeconds(ctx, h.ID, time.Now())
	if err != nil {
		return nil, err
	}
	h.ElapsedSeconds = elapsed
	return h, nil
}

// hourSessionElapsedSeconds soma os intervalos "rodando" (start/resume até o
// próximo pause/end, ou até `now` se ainda não fechou) a partir do histórico
// de eventos — nunca de um contador guardado.
func (s *Server) hourSessionElapsedSeconds(ctx context.Context, sessionID string, now time.Time) (int64, error) {
	rows, err := s.db.Query(ctx, `
		SELECT event_type, created_at FROM hour_session_events
		WHERE session_id = $1::uuid ORDER BY created_at`, sessionID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	events := []hourSessionEvent{}
	for rows.Next() {
		var e hourSessionEvent
		if err := rows.Scan(&e.EventType, &e.CreatedAt); err != nil {
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
		}
	}
	if running {
		total += now.Sub(since)
	}
	if total < 0 {
		return 0
	}
	return int64(total.Seconds())
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
// transação. Saldo pode ficar negativo (sem corte automático, por design).
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
	if _, err := tx.Exec(ctx, `
		UPDATE hour_sessions SET status = 'ended', updated_at = now() WHERE id = $1::uuid`,
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
		UPDATE hour_clients SET balance_minutes = balance_minutes - $2, updated_at = now()
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
