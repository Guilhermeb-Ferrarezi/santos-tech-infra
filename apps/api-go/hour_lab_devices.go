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

// upsertLabDeviceHeartbeat grava/atualiza o dispositivo e devolve, na mesma
// query, os comandos pendentes ANTES de zerar unpair_requested/pending_pair_token
// — assim cada comando é entregue exatamente uma vez (a query seguinte já não
// os vê mais). message_id/text não são zerados: o app deduplica localmente
// pelo id.
func (s *Server) upsertLabDeviceHeartbeat(ctx context.Context, deviceUUID, ip, appVersion string, sessionID *string) (*LabDeviceHeartbeatResult, error) {
	var res LabDeviceHeartbeatResult
	err := s.db.QueryRow(ctx, `
		WITH upserted AS (
			INSERT INTO hour_lab_devices (device_uuid, last_seen_at, last_ip, app_version, current_session_id)
			VALUES ($1, now(), $2, $3, $4::uuid)
			ON CONFLICT (device_uuid) DO UPDATE SET
				last_seen_at = now(), last_ip = $2, app_version = $3, current_session_id = $4::uuid
			RETURNING name, unpair_requested, message_id::text, message_text, pending_pair_token
		), cleared AS (
			UPDATE hour_lab_devices SET unpair_requested = false, pending_pair_token = NULL
			WHERE device_uuid = $1 AND (unpair_requested = true OR pending_pair_token IS NOT NULL)
		)
		SELECT name, unpair_requested, message_id, message_text, pending_pair_token FROM upserted`,
		deviceUUID, ip, appVersion, sessionID).
		Scan(&res.Name, &res.UnpairRequested, &res.MessageID, &res.MessageText, &res.PairToken)
	if err != nil {
		return nil, err
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
