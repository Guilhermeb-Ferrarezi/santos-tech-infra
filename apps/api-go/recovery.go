package main

import "context"

func (s *Server) insertRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	for _, h := range hashes {
		if _, err := s.db.Exec(ctx,
			`INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1,$2)`, userID, h); err != nil {
			return err
		}
	}
	return nil
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
