package main

// PCs do laboratório rodando o app desktop (hour-timer-app). Cada instalação
// tem um device_uuid estável gerado no primeiro boot; o app manda heartbeat
// periódico e este arquivo faz a contraparte no banco. Nome é sempre
// atribuído pelo admin (nunca pelo próprio PC, ver hour_lab_devices no
// schema) — o app só exibe de volta o que o backend devolver.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type LabDevice struct {
	ID                string     `json:"id"`
	DeviceUUID        string     `json:"deviceUuid"`
	Name              *string    `json:"name"`
	LastSeenAt        *time.Time `json:"lastSeenAt"`
	LastIP            *string    `json:"lastIp"`
	AppVersion        *string    `json:"appVersion"`
	CurrentSessionID  *string    `json:"currentSessionId"`
	CurrentClientName *string    `json:"currentClientName"`
	CurrentStatus     *string    `json:"currentStatus"`
	CreatedAt         time.Time  `json:"createdAt"`
}

// LabDeviceHeartbeatResult é o que o app precisa saber a cada heartbeat: nome
// atribuído (pra exibir), e comandos pendentes do admin.
type LabDeviceHeartbeatResult struct {
	Name            *string
	UnpairRequested bool
	MessageID       *string
	MessageText     *string
	PairToken       *string
}

var errLabDeviceNotFound = appErr(http.StatusNotFound, "LAB_DEVICE_NOT_FOUND", "Dispositivo não encontrado")

// labDeviceQuerier é o subconjunto de pgx.Tx que upsertLabDeviceHeartbeatTx usa.
// Existe pra o teste conseguir provar a entrega-exatamente-uma-vez sem um
// Postgres real (o harness de teste deste pacote roda com s.db == nil).
type labDeviceQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// labDeviceHeartbeatUpsertSQL grava/atualiza os campos de heartbeat e devolve os
// comandos pendentes. As colunas de comando (name, unpair_requested, message_*,
// pending_pair_token) NÃO entram no SET, então o RETURNING traz justamente os
// valores ANTIGOS — que é o que o app precisa receber.
//
// Antes isto era um único comando com dois CTEs (`upserted` fazendo
// INSERT...ON CONFLICT DO UPDATE e `cleared` fazendo UPDATE) sobre a MESMA
// linha: o Postgres aplica só uma das modificações por comando, e todos os
// sub-statements enxergam o mesmo snapshot — o `cleared` nunca tinha efeito e
// unpair_requested/pending_pair_token eram reentregues em TODO heartbeat,
// apesar do comentário prometer "entregue exatamente uma vez".
const labDeviceHeartbeatUpsertSQL = `
	INSERT INTO hour_lab_devices (device_uuid, last_seen_at, last_ip, app_version, current_session_id)
	VALUES ($1, now(), $2, $3, $4::uuid)
	ON CONFLICT (device_uuid) DO UPDATE SET
		last_seen_at = now(), last_ip = $2, app_version = $3, current_session_id = $4::uuid
	RETURNING name, unpair_requested, message_id::text, message_text, pending_pair_token`

// labDeviceHeartbeatClearSQL roda como comando SEPARADO, na mesma transação do
// upsert — é assim que a limpeza de fato acontece. Como o upsert já segurou o
// lock da linha, um pareamento concorrente (pairLabDeviceViaQR) fica bloqueado
// até o commit e só então grava o token novo: nenhum token se perde.
const labDeviceHeartbeatClearSQL = `
	UPDATE hour_lab_devices SET unpair_requested = false, pending_pair_token = NULL
	WHERE device_uuid = $1 AND (unpair_requested = true OR pending_pair_token IS NOT NULL)`

// upsertLabDeviceHeartbeat grava/atualiza o dispositivo e devolve os comandos
// pendentes, zerando unpair_requested/pending_pair_token na mesma transação —
// assim cada comando é entregue exatamente uma vez. message_id/text não são
// zerados: o app deduplica localmente pelo id.
func (s *Server) upsertLabDeviceHeartbeat(ctx context.Context, deviceUUID, ip, appVersion string, sessionID *string) (*LabDeviceHeartbeatResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do Commit
	res, err := upsertLabDeviceHeartbeatTx(ctx, tx, deviceUUID, ip, appVersion, sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func upsertLabDeviceHeartbeatTx(ctx context.Context, tx labDeviceQuerier, deviceUUID, ip, appVersion string, sessionID *string) (*LabDeviceHeartbeatResult, error) {
	var res LabDeviceHeartbeatResult
	if err := tx.QueryRow(ctx, labDeviceHeartbeatUpsertSQL, deviceUUID, ip, appVersion, sessionID).
		Scan(&res.Name, &res.UnpairRequested, &res.MessageID, &res.MessageText, &res.PairToken); err != nil {
		return nil, err
	}
	if res.UnpairRequested || res.PairToken != nil {
		if _, err := tx.Exec(ctx, labDeviceHeartbeatClearSQL, deviceUUID); err != nil {
			return nil, err
		}
	}
	return &res, nil
}

const labDeviceCols = `d.id::text, d.device_uuid, d.name, d.last_seen_at, d.last_ip, d.app_version,
	d.current_session_id::text, c.name, s.status, d.created_at`

func scanLabDevice(row pgx.Row) (*LabDevice, error) {
	var d LabDevice
	err := row.Scan(&d.ID, &d.DeviceUUID, &d.Name, &d.LastSeenAt, &d.LastIP, &d.AppVersion,
		&d.CurrentSessionID, &d.CurrentClientName, &d.CurrentStatus, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// listLabDevices traz todos os PCs já vistos ao menos uma vez, com a sessão
// atual (se houver) — "online/offline" é decidido pelo front a partir de
// lastSeenAt (o backend não guarda esse estado, só o fato observado).
func (s *Server) listLabDevices(ctx context.Context) ([]LabDevice, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+labDeviceCols+`
		FROM hour_lab_devices d
		LEFT JOIN hour_sessions s ON s.id = d.current_session_id
		LEFT JOIN hour_clients c ON c.id = s.client_id
		ORDER BY d.name NULLS LAST, d.device_uuid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LabDevice{}
	for rows.Next() {
		d, err := scanLabDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Server) renameLabDevice(ctx context.Context, id, name string) (*LabDevice, error) {
	tag, err := s.db.Exec(ctx, `UPDATE hour_lab_devices SET name = $2 WHERE id = $1::uuid`, id, name)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, errLabDeviceNotFound
	}
	return scanLabDevice(s.db.QueryRow(ctx, `
		SELECT `+labDeviceCols+`
		FROM hour_lab_devices d
		LEFT JOIN hour_sessions s ON s.id = d.current_session_id
		LEFT JOIN hour_clients c ON c.id = s.client_id
		WHERE d.id = $1::uuid`, id))
}

// requestLabDeviceUnpair pede pro PC voltar sozinho pra tela de colar link no
// próximo heartbeat — não derruba a sessão em si (isso é feito por
// endHourSession/transitionHourSession), só solta o pareamento local do PC.
func (s *Server) requestLabDeviceUnpair(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE hour_lab_devices SET unpair_requested = true WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

// pairLabDeviceViaQR inicia uma sessão nova pro cliente escolhido (mesma
// lógica de startHourSession) e deixa o token pronto pra entrega no próximo
// heartbeat do PC — fluxo do QR: PC mostra um QR com o próprio device_uuid,
// admin escaneia com o celular (já logado) em /admin/horas/parear/:deviceUuid,
// escolhe o cliente, e o PC recebe o token sozinho, sem digitar nada.
func (s *Server) pairLabDeviceViaQR(ctx context.Context, deviceUUID, clientID string, createdBy int64) (*HourSession, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hour_lab_devices WHERE device_uuid = $1)`, deviceUUID).
		Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, errLabDeviceNotFound
	}
	// Checagem de existência acima, não uma transação com o INSERT/UPDATE
	// abaixo: janela de corrida (o PC some entre as duas queries) é
	// improvável e inofensiva — na pior hipótese a sessão fica sem o token
	// entregue, e o admin repete o pareamento.
	h, token, _, err := s.startHourSession(ctx, clientID, createdBy)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE hour_lab_devices SET pending_pair_token = $2 WHERE device_uuid = $1`,
		deviceUUID, token); err != nil {
		return nil, err
	}
	return h, nil
}

// sendLabDeviceMessage grava um aviso pro PC mostrar no próximo heartbeat.
// message_id novo a cada envio é o que permite mandar o mesmo texto de novo
// (o app só reexibe quando o id muda em relação ao último que já mostrou).
func (s *Server) sendLabDeviceMessage(ctx context.Context, id, text string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_lab_devices
		SET message_id = gen_random_uuid(), message_text = $2, message_sent_at = now()
		WHERE id = $1::uuid`,
		id, text)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}
