package main

// Inventário de software dos PCs do laboratório. O app (hour-timer-app) lê a
// lista de programas instalados do registro do Windows e manda inteira; aqui
// ela é guardada como foto do estado atual e cruzada com os programas que o
// admin cadastrou como esperados (hour_lab_expected_programs).
//
// A expectativa mora no servidor de propósito: mudar a lista de programas que
// um PC do lab deve ter é edição no dashboard, não instalador novo.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// LabProgram é uma entrada do inventário — o que o Windows mostra em
// "Adicionar ou remover programas".
type LabProgram struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
}

// ExpectedProgram é um programa que o admin espera encontrar nos PCs.
type ExpectedProgram struct {
	ID           int64     `json:"id"`
	Label        string    `json:"label"`
	MatchPattern string    `json:"matchPattern"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ExpectedProgramStatus é o cruzamento: um esperado + se o PC tem, e qual
// entrada do inventário casou (o nome instalado costuma trazer a versão junto,
// e é isso que o admin quer ver na tela).
type ExpectedProgramStatus struct {
	ID             int64  `json:"id"`
	Label          string `json:"label"`
	MatchPattern   string `json:"matchPattern"`
	Installed      bool   `json:"installed"`
	MatchedName    string `json:"matchedName,omitempty"`
	MatchedVersion string `json:"matchedVersion,omitempty"`
}

// DeviceInventory é a resposta da tela de programas de um PC.
type DeviceInventory struct {
	CollectedAt *time.Time              `json:"collectedAt"`
	Expected    []ExpectedProgramStatus `json:"expected"`
	Installed   []LabProgram            `json:"installed"`
}

// Tetos da coleta. A rota é pública (autenticada só pelo segredo do
// dispositivo), então o payload precisa de limite: um PC real tem algumas
// centenas de entradas no registro.
const (
	maxInventoryPrograms   = 1000
	maxInventoryFieldRunes = 200
)

var errTooManyPrograms = appErr(http.StatusBadRequest, "INVENTORY_TOO_LARGE",
	"Inventário grande demais")

// replaceLabDeviceInventory autentica o dispositivo pelo segredo (mesma regra do
// heartbeat) e troca o inventário inteiro numa transação. Substituição total, e
// não merge: programa desinstalado tem que sumir da tela, e o app não tem como
// saber o que mudou desde a última coleta.
func (s *Server) replaceLabDeviceInventory(ctx context.Context, deviceUUID, deviceSecret string, programs []LabProgram) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do Commit

	// O segredo é a única credencial do PC; sem ele nem lemos o inventário
	// antigo. Diferente do heartbeat, aqui NÃO adotamos dispositivo novo:
	// inventário de um device_uuid que nunca bateu heartbeat não tem dono.
	minted, err := authLabDeviceTx(ctx, tx, deviceUUID, deviceSecret)
	if err != nil {
		return err
	}
	if minted != nil {
		return errLabDeviceUnauthorized
	}

	var deviceID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM hour_lab_devices WHERE device_uuid = $1`, deviceUUID).Scan(&deviceID)
	if err == pgx.ErrNoRows {
		return errLabDeviceNotFound
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM hour_lab_device_programs WHERE device_id = $1::uuid`, deviceID); err != nil {
		return err
	}
	for _, p := range programs {
		name := truncRunes(strings.TrimSpace(p.Name), maxInventoryFieldRunes)
		if name == "" {
			continue
		}
		// ON CONFLICT: a chave é (device_id, name, version) e o registro do
		// Windows repete a mesma entrada em HKLM e HKCU com frequência.
		if _, err := tx.Exec(ctx, `
			INSERT INTO hour_lab_device_programs (device_id, name, version, publisher)
			VALUES ($1::uuid, $2, $3, $4)
			ON CONFLICT (device_id, name, version) DO NOTHING`,
			deviceID, name,
			truncRunes(strings.TrimSpace(p.Version), maxInventoryFieldRunes),
			truncRunes(strings.TrimSpace(p.Publisher), maxInventoryFieldRunes)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE hour_lab_devices SET inventory_collected_at = now() WHERE id = $1::uuid`, deviceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func truncRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// labDeviceInventory devolve o inventário do PC já cruzado com os esperados.
// O casamento é feito em SQL (ILIKE '%pattern%') pra não trazer as duas listas
// inteiras só para comparar em memória.
func (s *Server) labDeviceInventory(ctx context.Context, deviceID string) (*DeviceInventory, error) {
	var collectedAt *time.Time
	err := s.db.QueryRow(ctx,
		`SELECT inventory_collected_at FROM hour_lab_devices WHERE id = $1::uuid`, deviceID).Scan(&collectedAt)
	if err == pgx.ErrNoRows {
		return nil, errLabDeviceNotFound
	}
	if err != nil {
		return nil, err
	}

	out := &DeviceInventory{
		CollectedAt: collectedAt,
		Expected:    []ExpectedProgramStatus{},
		Installed:   []LabProgram{},
	}

	// DISTINCT ON: um pattern pode casar várias entradas ("Unity Hub" e
	// "Unity 6000"); a tela mostra uma, então escolhemos a primeira em ordem
	// alfabética para o resultado não dançar entre requisições.
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT ON (e.id) e.id, e.label, e.match_pattern, p.name, p.version
		FROM hour_lab_expected_programs e
		LEFT JOIN hour_lab_device_programs p
		  ON p.device_id = $1::uuid AND p.name ILIKE '%' || e.match_pattern || '%'
		ORDER BY e.id, p.name`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st ExpectedProgramStatus
		var name, version *string
		if err := rows.Scan(&st.ID, &st.Label, &st.MatchPattern, &name, &version); err != nil {
			return nil, err
		}
		if name != nil {
			st.Installed = true
			st.MatchedName = *name
			if version != nil {
				st.MatchedVersion = *version
			}
		}
		out.Expected = append(out.Expected, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A ordem de label só é aplicada agora porque o DISTINCT ON exige que o
	// ORDER BY comece pela expressão distinta.
	sortExpectedByLabel(out.Expected)

	prows, err := s.db.Query(ctx, `
		SELECT name, version, publisher FROM hour_lab_device_programs
		WHERE device_id = $1::uuid ORDER BY name`, deviceID)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var p LabProgram
		if err := prows.Scan(&p.Name, &p.Version, &p.Publisher); err != nil {
			return nil, err
		}
		out.Installed = append(out.Installed, p)
	}
	return out, prows.Err()
}

func sortExpectedByLabel(items []ExpectedProgramStatus) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && strings.ToLower(items[j].Label) < strings.ToLower(items[j-1].Label); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// ── programas esperados (CRUD do admin) ──────────────────────────────────────

func (s *Server) listExpectedPrograms(ctx context.Context) ([]ExpectedProgram, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, label, match_pattern, created_at, updated_at
		FROM hour_lab_expected_programs ORDER BY lower(label)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExpectedProgram{}
	for rows.Next() {
		var e ExpectedProgram
		if err := rows.Scan(&e.ID, &e.Label, &e.MatchPattern, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Server) createExpectedProgram(ctx context.Context, label, pattern string) (*ExpectedProgram, error) {
	var e ExpectedProgram
	err := s.db.QueryRow(ctx, `
		INSERT INTO hour_lab_expected_programs (label, match_pattern) VALUES ($1, $2)
		RETURNING id, label, match_pattern, created_at, updated_at`, label, pattern).
		Scan(&e.ID, &e.Label, &e.MatchPattern, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Server) updateExpectedProgram(ctx context.Context, id int64, label, pattern string) (*ExpectedProgram, error) {
	var e ExpectedProgram
	err := s.db.QueryRow(ctx, `
		UPDATE hour_lab_expected_programs SET label = $2, match_pattern = $3, updated_at = now()
		WHERE id = $1
		RETURNING id, label, match_pattern, created_at, updated_at`, id, label, pattern).
		Scan(&e.ID, &e.Label, &e.MatchPattern, &e.CreatedAt, &e.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, errExpectedProgramNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Server) deleteExpectedProgram(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM hour_lab_expected_programs WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errExpectedProgramNotFound
	}
	return nil
}

var errExpectedProgramNotFound = appErr(http.StatusNotFound, "EXPECTED_PROGRAM_NOT_FOUND",
	"Programa esperado não encontrado")
