package main

// Arquivos (Google Drive) — camada de banco. O conteúdo real mora no Drive
// (ver drive.go); aqui só ficam metadados de pasta e a ACL de quem enxerga/
// envia arquivo em cada uma. Segue o estilo cru (s.db, ids como string com
// ::uuid) de boards.go/social.go — entidades UUID deste repo não passam por
// s.q (ver uuidRe em handlers_boards.go, usado em todo handler de UUID).

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type DriveFolder struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	DriveFolderID string    `json:"driveFolderId"`
	CreatedBy     int64     `json:"createdBy"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// MyDriveFolder é a visão de uma pasta pro usuário comum: sem createdBy, com o
// nível de acesso efetivo já resolvido (read|write).
type MyDriveFolder struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	DriveFolderID string    `json:"driveFolderId"`
	Access        string    `json:"access"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// DriveFolderRoleAccess é uma entrada de ACL por cargo — fixo ("1"=Student,
// "2"=Teacher) ou personalizado (role_value = UUID do cargo).
type DriveFolderRoleAccess struct {
	RoleKind  string `json:"roleKind"`
	RoleValue string `json:"roleValue"`
	Access    string `json:"access"`
}

// DriveFolderMemberInput/Access: ACL por usuário individual (input do admin /
// saída enriquecida com nome+email pra tela de gestão).
type DriveFolderMemberInput struct {
	UserID int64  `json:"userId"`
	Access string `json:"access"`
}

type DriveFolderMemberAccess struct {
	UserID  int64     `json:"userId"`
	Name    string    `json:"name"`
	Email   string    `json:"email"`
	Access  string    `json:"access"`
	AddedAt time.Time `json:"addedAt"`
}

func (s *Server) listDriveFolders(ctx context.Context) ([]DriveFolder, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, name, COALESCE(description, ''), drive_folder_id, created_by, created_at, updated_at
		FROM drive_folders ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DriveFolder{}
	for rows.Next() {
		var f DriveFolder
		if err := rows.Scan(&f.ID, &f.Name, &f.Description, &f.DriveFolderID, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Server) getDriveFolder(ctx context.Context, id string) (*DriveFolder, error) {
	var f DriveFolder
	err := s.db.QueryRow(ctx, `
		SELECT id::text, name, COALESCE(description, ''), drive_folder_id, created_by, created_at, updated_at
		FROM drive_folders WHERE id = $1::uuid`, id).
		Scan(&f.ID, &f.Name, &f.Description, &f.DriveFolderID, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *Server) insertDriveFolder(ctx context.Context, name, description, driveFolderID string, createdBy int64) (*DriveFolder, error) {
	var f DriveFolder
	err := s.db.QueryRow(ctx, `
		INSERT INTO drive_folders (name, description, drive_folder_id, created_by)
		VALUES ($1, NULLIF($2, ''), $3, $4)
		RETURNING id::text, name, COALESCE(description, ''), drive_folder_id, created_by, created_at, updated_at`,
		name, description, driveFolderID, createdBy).
		Scan(&f.ID, &f.Name, &f.Description, &f.DriveFolderID, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// updateDriveFolderRow devolve nil (sem erro) se a pasta não existir.
func (s *Server) updateDriveFolderRow(ctx context.Context, id, name, description, driveFolderID string) (*DriveFolder, error) {
	var f DriveFolder
	err := s.db.QueryRow(ctx, `
		UPDATE drive_folders SET name = $2, description = NULLIF($3, ''), drive_folder_id = $4, updated_at = now()
		WHERE id = $1::uuid
		RETURNING id::text, name, COALESCE(description, ''), drive_folder_id, created_by, created_at, updated_at`,
		id, name, description, driveFolderID).
		Scan(&f.ID, &f.Name, &f.Description, &f.DriveFolderID, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *Server) deleteDriveFolderRow(ctx context.Context, id string) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM drive_folders WHERE id = $1::uuid`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Server) listDriveFolderRoleAccess(ctx context.Context, folderID string) ([]DriveFolderRoleAccess, error) {
	rows, err := s.db.Query(ctx, `
		SELECT role_kind, role_value, access FROM drive_folder_role_access
		WHERE folder_id = $1::uuid ORDER BY role_kind, role_value`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DriveFolderRoleAccess{}
	for rows.Next() {
		var a DriveFolderRoleAccess
		if err := rows.Scan(&a.RoleKind, &a.RoleValue, &a.Access); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Server) listDriveFolderMemberAccess(ctx context.Context, folderID string) ([]DriveFolderMemberAccess, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.user_id, u.name, u.email, m.access, m.added_at
		FROM drive_folder_members m JOIN users u ON u.id = m.user_id
		WHERE m.folder_id = $1::uuid ORDER BY m.added_at`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DriveFolderMemberAccess{}
	for rows.Next() {
		var m DriveFolderMemberAccess
		if err := rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Access, &m.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// replaceDriveFolderAccess substitui TODA a ACL de uma pasta (cargos + membros)
// numa transação — o admin manda o conjunto completo desejado, não deltas.
func (s *Server) replaceDriveFolderAccess(ctx context.Context, folderID string, roles []DriveFolderRoleAccess, members []DriveFolderMemberInput) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM drive_folder_role_access WHERE folder_id = $1::uuid`, folderID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM drive_folder_members WHERE folder_id = $1::uuid`, folderID); err != nil {
		return err
	}
	for _, ra := range roles {
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive_folder_role_access (folder_id, role_kind, role_value, access)
			VALUES ($1::uuid, $2, $3, $4)`, folderID, ra.RoleKind, ra.RoleValue, ra.Access); err != nil {
			return err
		}
	}
	for _, m := range members {
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive_folder_members (folder_id, user_id, access)
			VALUES ($1::uuid, $2, $3)`, folderID, m.UserID, m.Access); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// listDriveFoldersForUser devolve as pastas com acesso >= read pra um usuário
// NÃO-admin (admin vê tudo com write, resolvido em Go pelo handler chamador —
// ver handleListMyDriveFolders — pra não precisar de um caminho especial aqui).
func (s *Server) listDriveFoldersForUser(ctx context.Context, userID int64, role int16, customRoleID *string) ([]MyDriveFolder, error) {
	crID := ""
	if customRoleID != nil {
		crID = *customRoleID
	}
	rows, err := s.db.Query(ctx, `
		WITH acl AS (
			SELECT folder_id, access FROM drive_folder_role_access
			WHERE (role_kind = 'fixed' AND role_value = $2)
			   OR (role_kind = 'custom' AND $3 != '' AND role_value = $3)
			UNION ALL
			SELECT folder_id, access FROM drive_folder_members WHERE user_id = $1
		),
		best AS (
			SELECT folder_id, MAX(CASE WHEN access = 'write' THEN 2 ELSE 1 END) AS lvl
			FROM acl GROUP BY folder_id
		)
		SELECT f.id::text, f.name, COALESCE(f.description, ''), f.drive_folder_id, f.created_at, f.updated_at,
		       CASE b.lvl WHEN 2 THEN 'write' ELSE 'read' END
		FROM drive_folders f JOIN best b ON b.folder_id = f.id
		ORDER BY f.name`,
		userID, strconv.Itoa(int(role)), crID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MyDriveFolder{}
	for rows.Next() {
		var f MyDriveFolder
		if err := rows.Scan(&f.ID, &f.Name, &f.Description, &f.DriveFolderID, &f.CreatedAt, &f.UpdatedAt, &f.Access); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// effectiveDriveFolderAccess resolve o nível de acesso de um usuário NÃO-admin
// numa pasta específica: "" (sem acesso), "read" ou "write". Admin não passa
// por aqui — sempre "write", checado no chamador (folderAccessGuard, drive_access.go).
func (s *Server) effectiveDriveFolderAccess(ctx context.Context, folderID string, userID int64, role int16, customRoleID *string) (string, error) {
	crID := ""
	if customRoleID != nil {
		crID = *customRoleID
	}
	var lvl int
	err := s.db.QueryRow(ctx, `
		WITH acl AS (
			SELECT access FROM drive_folder_role_access
			WHERE folder_id = $1::uuid
			  AND ((role_kind = 'fixed' AND role_value = $3)
			       OR (role_kind = 'custom' AND $4 != '' AND role_value = $4))
			UNION ALL
			SELECT access FROM drive_folder_members WHERE folder_id = $1::uuid AND user_id = $2
		)
		SELECT COALESCE(MAX(CASE WHEN access = 'write' THEN 2 ELSE 1 END), 0) FROM acl`,
		folderID, userID, strconv.Itoa(int(role)), crID).Scan(&lvl)
	if err != nil {
		return "", err
	}
	switch lvl {
	case 2:
		return "write", nil
	case 1:
		return "read", nil
	default:
		return "", nil
	}
}
