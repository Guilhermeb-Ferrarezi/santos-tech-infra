package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/santos-tech/auth/db"
)

// Fila + auditoria das ações remotas nos PCs (tabela hour_lab_device_commands).
// Entrega at-most-once: o comando roda como SYSTEM no PC, então reentregar
// depois de uma resposta perdida (e reexecutar) é pior que perder — quem
// mandou vê "entregue, sem resultado" no histórico.

type LabCommand struct{ ID, Text string }

type LabCommandEntry struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Source      string     `json:"source"`
	Text        string     `json:"text"`
	Result      *string    `json:"result"`
	UserID      *int64     `json:"userId"`
	UserName    string     `json:"userName"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeliveredAt *time.Time `json:"deliveredAt"`
	ResultAt    *time.Time `json:"resultAt"`
}

type labCommandStore interface {
	Enqueue(ctx context.Context, deviceID string, userID int64, source, text string) (string, error)
	Audit(ctx context.Context, deviceID string, userID int64, source, kind, text string) error
	ClaimNext(ctx context.Context, deviceUUID string) (*LabCommand, error) // nil,nil = fila vazia
	StoreResult(ctx context.Context, deviceUUID, commandID, result string) (bool, error)
	List(ctx context.Context, deviceID string, limit int32) ([]LabCommandEntry, error)
	Get(ctx context.Context, deviceID, commandID string) (*LabCommandEntry, error) // nil,nil = não achou
}

type sqlLabCommandStore struct{ q *db.Queries }

func int4(v int64) pgtype.Int4 { return pgtype.Int4{Int32: int32(v), Valid: v > 0} }

func (s sqlLabCommandStore) Enqueue(ctx context.Context, deviceID string, userID int64, source, text string) (string, error) {
	id, err := s.q.EnqueueLabDeviceCommand(ctx, db.EnqueueLabDeviceCommandParams{
		DeviceID: uuidToPg(deviceID), UserID: int4(userID), Source: source, Text: text,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // FK: device não existe
		return "", errLabDeviceNotFound
	}
	if err != nil {
		return "", err
	}
	// Espelho legado (dashboard atual lê command_* da listagem) — best-effort.
	_ = s.q.MirrorLabDeviceLegacyCommand(ctx, db.MirrorLabDeviceLegacyCommandParams{
		CommandID: uuidToPg(id), Text: &text, DeviceID: uuidToPg(deviceID),
	})
	return id, nil
}

func (s sqlLabCommandStore) Audit(ctx context.Context, deviceID string, userID int64, source, kind, text string) error {
	return s.q.InsertLabDeviceAudit(ctx, db.InsertLabDeviceAuditParams{
		DeviceID: uuidToPg(deviceID), UserID: int4(userID), Source: source, Kind: kind, Text: text,
	})
}

func (s sqlLabCommandStore) ClaimNext(ctx context.Context, deviceUUID string) (*LabCommand, error) {
	row, err := s.q.ClaimNextLabDeviceCommand(ctx, deviceUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &LabCommand{ID: row.CID, Text: row.Text}, nil
}

func (s sqlLabCommandStore) StoreResult(ctx context.Context, deviceUUID, commandID, result string) (bool, error) {
	if !uuidRe.MatchString(commandID) {
		return false, nil
	}
	n, err := s.q.StoreLabDeviceCommandQueueResult(ctx, db.StoreLabDeviceCommandQueueResultParams{
		Result: &result, DeviceUuid: deviceUUID, CommandID: uuidToPg(commandID),
	})
	return n > 0, err
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func entryFrom(id, kind, source, text string, result *string, userID pgtype.Int4, userName string,
	created time.Time, delivered, resultAt pgtype.Timestamptz) LabCommandEntry {
	e := LabCommandEntry{ID: id, Kind: kind, Source: source, Text: text, Result: result, UserName: userName,
		CreatedAt: created, DeliveredAt: tsPtr(delivered), ResultAt: tsPtr(resultAt)}
	if userID.Valid {
		u := int64(userID.Int32)
		e.UserID = &u
	}
	return e
}

func (s sqlLabCommandStore) List(ctx context.Context, deviceID string, limit int32) ([]LabCommandEntry, error) {
	rows, err := s.q.ListLabDeviceCommands(ctx, db.ListLabDeviceCommandsParams{DeviceID: uuidToPg(deviceID), Lim: limit})
	if err != nil {
		return nil, err
	}
	out := make([]LabCommandEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, entryFrom(r.CID, r.Kind, r.Source, r.Text, r.Result, r.UserID, r.UserName, r.CreatedAt.Time, r.DeliveredAt, r.ResultAt))
	}
	return out, nil
}

func (s sqlLabCommandStore) Get(ctx context.Context, deviceID, commandID string) (*LabCommandEntry, error) {
	r, err := s.q.GetLabDeviceCommand(ctx, db.GetLabDeviceCommandParams{DeviceID: uuidToPg(deviceID), ID: uuidToPg(commandID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e := entryFrom(r.CID, r.Kind, r.Source, r.Text, r.Result, r.UserID, r.UserName, r.CreatedAt.Time, r.DeliveredAt, r.ResultAt)
	return &e, nil
}
