package main

import (
	"context"
	"fmt"
	"strings"
)

// insertRecoveryCodes insere todos os hashes em um único INSERT multi-row,
// garantindo atomicidade: ou todos são criados ou nenhum (sem estado parcial).
func (s *Server) insertRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	args := make([]any, 0, len(hashes)*2)
	placeholders := make([]string, len(hashes))
	for i, h := range hashes {
		placeholders[i] = fmt.Sprintf("($%d,$%d)", i*2+1, i*2+2)
		args = append(args, userID, h)
	}
	_, err := s.db.Exec(ctx,
		"INSERT INTO recovery_codes (user_id, code_hash) VALUES "+strings.Join(placeholders, ","),
		args...)
	return err
}

// consumeRecoveryCode marca um código como usado, se existir e estiver disponível.
func (s *Server) consumeRecoveryCode(ctx context.Context, userID int64, codeHash string) bool {
	ct, err := s.db.Exec(ctx,
		`UPDATE recovery_codes SET used_at = now() WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL`,
		userID, codeHash)
	return err == nil && ct.RowsAffected() == 1
}

func (s *Server) deleteRecoveryCodes(ctx context.Context, userID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id=$1`, userID)
	return err
}
