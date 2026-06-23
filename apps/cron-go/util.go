package main

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func pgTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
