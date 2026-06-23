package main

import (
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func pgTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// redactSensitiveRe mascara valores de chaves sensíveis em strings JSON/form.
// Chaves: password, passwd, token, secret, authorization, cvv, card, key.
var redactSensitiveRe = regexp.MustCompile(
	`(?i)("(?:password|passwd|token|secret|authorization|cvv|card(?:_number)?|api_key|key)"\s*:\s*)"[^"]*"`,
)

// redact substitui valores de campos sensíveis por "***" numa string arbitrária.
// Conservador: só redige padrões JSON explícitos; não toca em outros formatos.
func redact(s string) string {
	return redactSensitiveRe.ReplaceAllString(s, `$1"***"`)
}
