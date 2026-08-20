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
	"encoding/base64"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// LabProgram é uma entrada do inventário — o que o Windows mostra em
// "Adicionar ou remover programas".
type LabProgram struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	// IconHash é o sha256 do PNG do ícone. Na resposta ao dashboard vira a URL
	// de /program-icons/{hash} — a imagem não trafega no JSON.
	//
	// Na COLETA o app manda só o hash: o ícone do Blender é idêntico em todo
	// PC, então reenviar os bytes a cada máquina seria repetir ~200 KB que o
	// servidor já tem. O que ele ainda não conhece volta em missingIcons e o
	// app manda só esses (ver POST /public/lab-devices/icons).
	IconHash string `json:"iconHash,omitempty"`
	// Icon (base64) é o formato da primeira versão, mantido porque os PCs com
	// app 0.1.5 já instalado continuam mandando assim — o servidor calcula o
	// hash e guarda na tabela de ícones do mesmo jeito.
	Icon string `json:"icon,omitempty"`
}

// LabIconUpload é um ícone que o servidor pediu (hash veio em missingIcons).
type LabIconUpload struct {
	Hash string `json:"hash"`
	PNG  string `json:"png"` // base64 puro
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
	// Hash do ícone do programa que casou; o dashboard monta a URL.
	MatchedIconHash string `json:"matchedIconHash,omitempty"`
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
	// Um PNG 48x48 tem uns 2-5 KB. 32 KB dá folga pra ícone mais detalhado e
	// ainda barra quem tente usar a tabela como depósito: acima disso o ícone é
	// descartado, não o programa.
	maxIconBytes = 32 << 10
	// Ícones por lote no POST /public/lab-devices/icons. Um PC estreando manda
	// ~60; 200 cobre isso e limita o corpo a poucos MB.
	maxIconsPerBatch = 200
)

var errTooManyPrograms = appErr(http.StatusBadRequest, "INVENTORY_TOO_LARGE",
	"Inventário grande demais")

// replaceLabDeviceInventory autentica o dispositivo pelo segredo (mesma regra do
// heartbeat) e troca o inventário inteiro numa transação. Substituição total, e
// não merge: programa desinstalado tem que sumir da tela, e o app não tem como
// saber o que mudou desde a última coleta.
// Devolve os hashes de ícone que o servidor AINDA não tem — o app manda só
// esses em seguida (POST /public/lab-devices/icons). Do segundo PC em diante a
// lista costuma vir vazia: os ícones já subiram na primeira máquina.
func (s *Server) replaceLabDeviceInventory(ctx context.Context, deviceUUID, deviceSecret string, programs []LabProgram) ([]string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do Commit

	// O segredo é a única credencial do PC; sem ele nem lemos o inventário
	// antigo. Diferente do heartbeat, aqui NÃO adotamos dispositivo novo:
	// inventário de um device_uuid que nunca bateu heartbeat não tem dono.
	minted, err := authLabDeviceTx(ctx, tx, deviceUUID, deviceSecret)
	if err != nil {
		return nil, err
	}
	if minted != nil {
		return nil, errLabDeviceUnauthorized
	}

	var deviceID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM hour_lab_devices WHERE device_uuid = $1`, deviceUUID).Scan(&deviceID)
	if err == pgx.ErrNoRows {
		return nil, errLabDeviceNotFound
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM hour_lab_device_programs WHERE device_id = $1::uuid`, deviceID); err != nil {
		return nil, err
	}
	// Set pra não repetir hash na resposta (vários programas do mesmo
	// fabricante compartilham o mesmo ícone).
	missing := map[string]bool{}
	for _, p := range programs {
		name := sanitizeInventoryField(p.Name)
		if name == "" {
			continue
		}
		hash, err := resolveIconHashTx(ctx, tx, p)
		if err != nil {
			return nil, err
		}
		// ON CONFLICT: a chave é (device_id, name, version) e o registro do
		// Windows repete a mesma entrada em HKLM e HKCU com frequência.
		if _, err := tx.Exec(ctx, `
			INSERT INTO hour_lab_device_programs (device_id, name, version, publisher, icon_hash)
			VALUES ($1::uuid, $2, $3, $4, $5)
			ON CONFLICT (device_id, name, version) DO NOTHING`,
			deviceID, name,
			sanitizeInventoryField(p.Version),
			sanitizeInventoryField(p.Publisher),
			nullIfEmpty(hash)); err != nil {
			return nil, err
		}
		if hash == "" {
			continue
		}
		var known bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM hour_lab_program_icons WHERE hash = $1)`, hash).Scan(&known); err != nil {
			return nil, err
		}
		if !known {
			missing[hash] = true
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE hour_lab_devices SET inventory_collected_at = now() WHERE id = $1::uuid`, deviceID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(missing))
	for h := range missing {
		out = append(out, h)
	}
	return out, nil
}

// resolveIconHashTx decide o hash do ícone de um programa e, quando o app manda
// os bytes (formato da 0.1.5), aproveita e guarda a imagem.
func resolveIconHashTx(ctx context.Context, tx labDeviceQuerier, p LabProgram) (string, error) {
	if p.Icon != "" {
		png, err := base64.StdEncoding.DecodeString(strings.TrimSpace(p.Icon))
		if err != nil || len(png) == 0 || len(png) > maxIconBytes {
			return "", nil // ícone inválido não invalida o programa
		}
		hash := sha256Hex(string(png))
		if err := storeIconTx(ctx, tx, hash, png); err != nil {
			return "", err
		}
		return hash, nil
	}
	return sanitizeIconHash(p.IconHash), nil
}

// storeIconTx grava a imagem uma única vez — a chave é o próprio conteúdo, então
// dois PCs com o mesmo Blender convergem para a mesma linha.
func storeIconTx(ctx context.Context, tx labDeviceQuerier, hash string, png []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO hour_lab_program_icons (hash, png) VALUES ($1, $2)
		ON CONFLICT (hash) DO NOTHING`, hash, png)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// sanitizeInventoryField limpa um campo vindo do registro do Windows.
//
// O NUL não é paranoia: o registro devolve entradas com \x00 no meio do nome
// ("Roblox Player for guibf\x00" é real), e o Postgres recusa 0x00 em coluna
// text (SQLSTATE 22021) — uma entrada assim derrubava o inventário INTEIRO da
// máquina com 500. Os demais caracteres de controle vão junto porque só sujam
// a tela do admin.
func sanitizeInventoryField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == 0 || unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return truncRunes(strings.TrimSpace(s), maxInventoryFieldRunes)
}

// sanitizeIconHash aceita só sha256 em hex minúsculo. O hash vira parte da URL
// /program-icons/{hash} e chave de busca no banco: qualquer coisa fora desse
// formato é descartada aqui, antes de chegar na query.
func sanitizeIconHash(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) != 64 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return s
}

func truncRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// storeLabProgramIcons grava as imagens que o servidor pediu em missingIcons.
// O hash é recalculado a partir dos bytes recebidos e comparado com o
// declarado: sem isso, um PC poderia gravar qualquer imagem sob o hash de outro
// programa e trocar o ícone de todo mundo (o armazenamento é compartilhado).
func (s *Server) storeLabProgramIcons(ctx context.Context, deviceUUID, deviceSecret string, icons []LabIconUpload) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do Commit

	minted, err := authLabDeviceTx(ctx, tx, deviceUUID, deviceSecret)
	if err != nil {
		return 0, err
	}
	if minted != nil {
		return 0, errLabDeviceUnauthorized
	}

	saved := 0
	for _, ic := range icons {
		png, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ic.PNG))
		if err != nil || len(png) == 0 || len(png) > maxIconBytes {
			continue
		}
		if sha256Hex(string(png)) != sanitizeIconHash(ic.Hash) {
			continue // hash não confere com o conteúdo: descarta
		}
		if err := storeIconTx(ctx, tx, sanitizeIconHash(ic.Hash), png); err != nil {
			return 0, err
		}
		saved++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return saved, nil
}

// labProgramIcon devolve o PNG cru de um hash, pro dashboard servir em <img>.
func (s *Server) labProgramIcon(ctx context.Context, hash string) ([]byte, error) {
	var png []byte
	err := s.db.QueryRow(ctx, `SELECT png FROM hour_lab_program_icons WHERE hash = $1`, hash).Scan(&png)
	if err == pgx.ErrNoRows {
		return nil, errIconNotFound
	}
	if err != nil {
		return nil, err
	}
	return png, nil
}

var errIconNotFound = appErr(http.StatusNotFound, "ICON_NOT_FOUND", "Ícone não encontrado")

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
		SELECT DISTINCT ON (e.id) e.id, e.label, e.match_pattern, p.name, p.version, p.icon_hash
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
		var name, version, icon *string
		if err := rows.Scan(&st.ID, &st.Label, &st.MatchPattern, &name, &version, &icon); err != nil {
			return nil, err
		}
		if name != nil {
			st.Installed = true
			st.MatchedName = *name
			if version != nil {
				st.MatchedVersion = *version
			}
			if icon != nil {
				st.MatchedIconHash = *icon
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
		SELECT name, version, publisher, coalesce(icon_hash, '') FROM hour_lab_device_programs
		WHERE device_id = $1::uuid ORDER BY name`, deviceID)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var p LabProgram
		if err := prows.Scan(&p.Name, &p.Version, &p.Publisher, &p.IconHash); err != nil {
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
