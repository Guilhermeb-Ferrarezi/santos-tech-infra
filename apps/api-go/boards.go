package main

// Quadros (Excalidraw) — camada de banco. Quadros são privados por dono, com
// compartilhamento por membro (viewer/editor). A cena viaja inteira como JSONB
// (imagens embutidas em base64), com controle otimista via scene_version.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Board é a visão de um quadro pro cliente. Scene só vai no GET de detalhe.
type Board struct {
	ID           string          `json:"id"`
	OwnerID      int64           `json:"ownerId"`
	OwnerName    string          `json:"ownerName"`
	Title        string          `json:"title"`
	Role         string          `json:"role"` // papel de quem pediu: owner|editor|viewer
	Shared       bool            `json:"shared"`
	SceneVersion int             `json:"sceneVersion"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	Scene        json.RawMessage `json:"scene,omitempty"`
}

// BoardMember é um membro na lista do dialog de compartilhamento.
type BoardMember struct {
	UserID  int64     `json:"userId"`
	Name    string    `json:"name"`
	Email   string    `json:"email"`
	Role    string    `json:"role"`
	AddedAt time.Time `json:"addedAt"`
}

// listBoardsForUser devolve os quadros do usuário (donos + compartilhados com
// ele), sem a cena, do mais recente pro mais antigo.
func (s *Server) listBoardsForUser(ctx context.Context, userID int64) ([]Board, error) {
	rows, err := s.db.Query(ctx, `
		SELECT b.id::text, b.owner_id, u.name, b.title,
		       CASE WHEN b.owner_id = $1 THEN 'owner' ELSE m.role END,
		       EXISTS (SELECT 1 FROM board_members WHERE board_id = b.id),
		       b.scene_version, b.created_at, b.updated_at
		FROM boards b
		JOIN users u ON u.id = b.owner_id
		LEFT JOIN board_members m ON m.board_id = b.id AND m.user_id = $1
		WHERE b.owner_id = $1 OR m.user_id IS NOT NULL
		ORDER BY b.updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Board{}
	for rows.Next() {
		var b Board
		if err := rows.Scan(&b.ID, &b.OwnerID, &b.OwnerName, &b.Title, &b.Role,
			&b.Shared, &b.SceneVersion, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// boardForUser carrega um quadro COM a cena, já resolvendo o papel de quem
// pediu. nil = não existe ou o usuário não tem acesso (indistinguíveis de
// propósito: pra quem não é membro o quadro "não existe").
func (s *Server) boardForUser(ctx context.Context, boardID string, userID int64) (*Board, error) {
	var b Board
	err := s.db.QueryRow(ctx, `
		SELECT b.id::text, b.owner_id, u.name, b.title,
		       CASE WHEN b.owner_id = $2 THEN 'owner' ELSE m.role END,
		       EXISTS (SELECT 1 FROM board_members WHERE board_id = b.id),
		       b.scene_version, b.created_at, b.updated_at, b.scene
		FROM boards b
		JOIN users u ON u.id = b.owner_id
		LEFT JOIN board_members m ON m.board_id = b.id AND m.user_id = $2
		WHERE b.id = $1::uuid AND (b.owner_id = $2 OR m.user_id IS NOT NULL)`,
		boardID, userID).
		Scan(&b.ID, &b.OwnerID, &b.OwnerName, &b.Title, &b.Role, &b.Shared,
			&b.SceneVersion, &b.CreatedAt, &b.UpdatedAt, &b.Scene)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

// boardRoleFor resolve só o papel do usuário num quadro ("" = sem acesso ou
// quadro inexistente) — mais leve que boardForUser quando a cena não importa.
func (s *Server) boardRoleFor(ctx context.Context, boardID string, userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(ctx, `
		SELECT CASE WHEN b.owner_id = $2 THEN 'owner' ELSE m.role END
		FROM boards b
		LEFT JOIN board_members m ON m.board_id = b.id AND m.user_id = $2
		WHERE b.id = $1::uuid AND (b.owner_id = $2 OR m.user_id IS NOT NULL)`,
		boardID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}

func (s *Server) insertBoard(ctx context.Context, ownerID int64, title string) (*Board, error) {
	var b Board
	err := s.db.QueryRow(ctx, `
		INSERT INTO boards (owner_id, title) VALUES ($1, $2)
		RETURNING id::text, owner_id, title, scene_version, created_at, updated_at`,
		ownerID, title).
		Scan(&b.ID, &b.OwnerID, &b.Title, &b.SceneVersion, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	b.Role = "owner"
	return &b, nil
}

// updateBoardScene salva a cena com concorrência otimista: só atualiza se o
// scene_version do banco for o esperado, devolvendo a versão nova. (0, nil)
// significa conflito — alguém salvou antes (o handler já garantiu o acesso).
func (s *Server) updateBoardScene(ctx context.Context, boardID string, scene []byte, expectedVersion int, title *string) (int, error) {
	var v int
	err := s.db.QueryRow(ctx, `
		UPDATE boards SET scene = $2::jsonb, scene_version = scene_version + 1,
		       title = COALESCE($4, title), updated_at = now()
		WHERE id = $1::uuid AND scene_version = $3
		RETURNING scene_version`,
		boardID, string(scene), expectedVersion, title).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

func (s *Server) deleteBoard(ctx context.Context, boardID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM boards WHERE id = $1::uuid`, boardID)
	return err
}

func (s *Server) listBoardMembers(ctx context.Context, boardID string) ([]BoardMember, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.user_id, u.name, u.email, m.role, m.added_at
		FROM board_members m JOIN users u ON u.id = m.user_id
		WHERE m.board_id = $1::uuid ORDER BY m.added_at`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BoardMember{}
	for rows.Next() {
		var m BoardMember
		if err := rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Role, &m.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// upsertBoardMember adiciona o membro ou, se já existir, atualiza o papel.
func (s *Server) upsertBoardMember(ctx context.Context, boardID string, userID int64, role string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO board_members (board_id, user_id, role) VALUES ($1::uuid, $2, $3)
		ON CONFLICT (board_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		boardID, userID, role)
	return err
}

func (s *Server) deleteBoardMember(ctx context.Context, boardID string, userID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM board_members WHERE board_id = $1::uuid AND user_id = $2`,
		boardID, userID)
	return err
}
