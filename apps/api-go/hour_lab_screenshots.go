package main

// Captura de tela sob demanda dos PCs do laboratório.
//
// O admin pede, o comando sai no próximo heartbeat (até ~30s), o PC tira a foto
// da tela, MOSTRA um aviso na própria tela e manda a imagem. Não há captura
// periódica nem silenciosa: quem está sentado na máquina vê que aconteceu, e
// fica registrado quem pediu — a tela de um PC de uso compartilhado pode conter
// dado pessoal de quem está usando, e isso é responsabilidade de quem opera.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// LabScreenshot é uma captura já registrada.
type LabScreenshot struct {
	ID          string     `json:"id"`
	Width       int        `json:"width"`
	Height      int        `json:"height"`
	Bytes       int        `json:"bytes"`
	RequestedBy *string    `json:"requestedBy"`
	RequestedAt *time.Time `json:"requestedAt"`
	CapturedAt  time.Time  `json:"capturedAt"`
	// URL assinada de leitura, curta — a imagem mora no R2, não no banco.
	URL string `json:"url"`
}

const (
	// JPEG de uma tela 1080p em qualidade média fica entre 200 e 500 KB; 6 MB
	// cobre 4K sem virar porta de entrada pra upload arbitrário.
	maxScreenshotBytes = 6 << 20
	// Validade da URL assinada entregue ao dashboard. Curta de propósito: é
	// conteúdo de tela, não um arquivo público.
	screenshotURLTTL = 10 * time.Minute
	// Histórico mantido por PC. Captura é diagnóstico do momento — guardar mês
	// de imagens de tela de gente usando a máquina seria acúmulo sem uso.
	screenshotsKeptPerDevice = 20
)

var (
	errScreenshotTooLarge = appErr(http.StatusRequestEntityTooLarge, "SCREENSHOT_TOO_LARGE",
		"Imagem grande demais")
	errScreenshotNotConfigured = appErr(http.StatusServiceUnavailable, "SCREENSHOT_STORAGE_UNSET",
		"Captura de tela indisponível: armazenamento (R2) não configurado no servidor")
	errNoScreenshotPending = appErr(http.StatusConflict, "SCREENSHOT_NOT_REQUESTED",
		"Nenhuma captura pendente para este dispositivo")
)

// requestLabDeviceScreenshot marca o pedido; a entrega é no próximo heartbeat.
// Regravar por cima de um pedido pendente é inofensivo (continua sendo uma
// captura), então não há erro de "já pediu".
func (s *Server) requestLabDeviceScreenshot(ctx context.Context, deviceID string, actorID int64) error {
	if s.r2 == nil {
		return errScreenshotNotConfigured
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_lab_devices
		SET screenshot_requested_at = now(), screenshot_requested_by = $2
		WHERE id = $1::uuid`, deviceID, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

// storeLabDeviceScreenshot recebe a imagem do PC e registra a captura.
//
// Exige que o pedido EXISTA (o heartbeat que entregou o comando ainda não
// zerou, ou zerou há pouco): a rota é pública e autenticada só pelo segredo do
// dispositivo, então sem essa checagem um PC comprometido poderia despejar
// imagens no armazenamento quando bem entendesse.
func (s *Server) storeLabDeviceScreenshot(ctx context.Context, deviceUUID, deviceSecret string, jpeg []byte, width, height int) (*LabScreenshot, error) {
	if s.r2 == nil {
		return nil, errScreenshotNotConfigured
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do Commit

	minted, err := authLabDeviceTx(ctx, tx, deviceUUID, deviceSecret)
	if err != nil {
		return nil, err
	}
	if minted != nil {
		return nil, errLabDeviceUnauthorized
	}

	var deviceID string
	var requestedAt *time.Time
	var requestedBy *int64
	err = tx.QueryRow(ctx, `
		SELECT id::text, screenshot_requested_at, screenshot_requested_by
		FROM hour_lab_devices WHERE device_uuid = $1`, deviceUUID).
		Scan(&deviceID, &requestedAt, &requestedBy)
	if err == pgx.ErrNoRows {
		return nil, errLabDeviceNotFound
	}
	if err != nil {
		return nil, err
	}
	// O heartbeat zera screenshot_requested_at ao entregar o comando, então
	// aqui ele já costuma estar nulo — a janela de aceite é o pedido mais
	// recente registrado no histórico. Ver screenshotPendingWindow.
	pending, err := screenshotPendingTx(ctx, tx, deviceID, requestedAt)
	if err != nil {
		return nil, err
	}
	if !pending {
		return nil, errNoScreenshotPending
	}

	key := fmt.Sprintf("lab-screenshots/%s/%d.jpg", deviceID, time.Now().UnixNano())
	if _, err := s.r2.Upload(ctx, key, "image/jpeg", jpeg); err != nil {
		return nil, err
	}

	var shot LabScreenshot
	var by *string
	err = tx.QueryRow(ctx, `
		INSERT INTO hour_lab_device_screenshots
			(device_id, object_key, width, height, bytes, requested_by, requested_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, width, height, bytes, requested_at, captured_at,
			(SELECT name FROM users WHERE id = $6)`,
		deviceID, key, width, height, len(jpeg), requestedBy, requestedAt).
		Scan(&shot.ID, &shot.Width, &shot.Height, &shot.Bytes, &shot.RequestedAt, &shot.CapturedAt, &by)
	if err != nil {
		return nil, err
	}
	shot.RequestedBy = by

	// Poda o histórico na mesma transação: sem isso a tabela (e o bucket) só
	// crescem, e captura antiga de tela não serve pra nada.
	if _, err := tx.Exec(ctx, `
		DELETE FROM hour_lab_device_screenshots
		WHERE device_id = $1::uuid AND id NOT IN (
			SELECT id FROM hour_lab_device_screenshots
			WHERE device_id = $1::uuid ORDER BY captured_at DESC LIMIT $2
		)`, deviceID, screenshotsKeptPerDevice); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	shot.URL = s.r2.PresignGet(key, screenshotURLTTL)
	return &shot, nil
}

// screenshotPendingWindow é quanto tempo depois do pedido o PC ainda pode
// mandar a imagem. Cobre o ciclo do heartbeat (~30s) mais a captura e o upload,
// com folga pra rede ruim — e fecha a janela pra uma imagem aparecer horas
// depois, sem ninguém esperando por ela.
const screenshotPendingWindow = 5 * time.Minute

// screenshotPendingTx diz se há pedido válido: ou a coluna ainda está marcada
// (heartbeat não passou), ou o último pedido registrado é recente.
func screenshotPendingTx(ctx context.Context, tx labDeviceQuerier, deviceID string, requestedAt *time.Time) (bool, error) {
	if requestedAt != nil && time.Since(*requestedAt) < screenshotPendingWindow {
		return true, nil
	}
	var last *time.Time
	err := tx.QueryRow(ctx, `
		SELECT screenshot_delivered_at FROM hour_lab_devices WHERE id = $1::uuid`, deviceID).Scan(&last)
	if err != nil {
		return false, err
	}
	return last != nil && time.Since(*last) < screenshotPendingWindow, nil
}

// listLabDeviceScreenshots devolve o histórico do PC, do mais recente pro mais
// antigo, com URL assinada pra cada imagem.
func (s *Server) listLabDeviceScreenshots(ctx context.Context, deviceID string) ([]LabScreenshot, error) {
	rows, err := s.db.Query(ctx, `
		SELECT s.id::text, s.object_key, s.width, s.height, s.bytes, u.name, s.requested_at, s.captured_at
		FROM hour_lab_device_screenshots s
		LEFT JOIN users u ON u.id = s.requested_by
		WHERE s.device_id = $1::uuid
		ORDER BY s.captured_at DESC`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LabScreenshot{}
	for rows.Next() {
		var shot LabScreenshot
		var key string
		if err := rows.Scan(&shot.ID, &key, &shot.Width, &shot.Height, &shot.Bytes,
			&shot.RequestedBy, &shot.RequestedAt, &shot.CapturedAt); err != nil {
			return nil, err
		}
		if s.r2 != nil {
			shot.URL = s.r2.PresignGet(key, screenshotURLTTL)
		}
		out = append(out, shot)
	}
	return out, rows.Err()
}

// decodeScreenshot valida o JPEG que veio em base64.
func decodeScreenshot(b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(raw) == 0 {
		return nil, appErr(http.StatusBadRequest, "BAD_REQUEST", "Imagem inválida")
	}
	if len(raw) > maxScreenshotBytes {
		return nil, errScreenshotTooLarge
	}
	// Assinatura JPEG (FF D8 FF): a imagem vai ser servida de volta pro
	// navegador, então o tipo declarado precisa bater com o conteúdo.
	if len(raw) < 3 || raw[0] != 0xFF || raw[1] != 0xD8 || raw[2] != 0xFF {
		return nil, appErr(http.StatusBadRequest, "BAD_REQUEST", "Imagem precisa ser JPEG")
	}
	return raw, nil
}
