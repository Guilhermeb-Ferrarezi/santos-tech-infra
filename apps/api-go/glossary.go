package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type GlossaryTerm struct {
	ID        string    `json:"id"`
	Term      string    `json:"term"`
	Definicao string    `json:"definicao"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type GlossaryTermInput struct {
	Term      string `json:"term"`
	Definicao string `json:"definicao"`
}

const glossaryCols = `id::text, term, definicao, created_at, updated_at`

func scanGlossaryTerm(row pgx.Row) (*GlossaryTerm, error) {
	var g GlossaryTerm
	err := row.Scan(&g.ID, &g.Term, &g.Definicao, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &g, err
}

func (s *Server) listGlossaryTerms(ctx context.Context) ([]GlossaryTerm, error) {
	rows, err := s.db.Query(ctx, `SELECT `+glossaryCols+` FROM glossary_terms ORDER BY term`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GlossaryTerm{}
	for rows.Next() {
		g, err := scanGlossaryTerm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (s *Server) insertGlossaryTerm(ctx context.Context, in GlossaryTermInput) (*GlossaryTerm, error) {
	term, err := scanGlossaryTerm(s.db.QueryRow(ctx,
		`INSERT INTO glossary_terms (term, definicao) VALUES ($1,$2) RETURNING `+glossaryCols,
		in.Term, in.Definicao))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return term, nil
}

func (s *Server) updateGlossaryTerm(ctx context.Context, id string, in GlossaryTermInput) (*GlossaryTerm, error) {
	term, err := scanGlossaryTerm(s.db.QueryRow(ctx, `
		UPDATE glossary_terms SET term=$2, definicao=$3, updated_at=now()
		WHERE id=$1::uuid
		RETURNING `+glossaryCols,
		id, in.Term, in.Definicao))
	if err != nil {
		return nil, portalDBErr(err)
	}
	return term, nil
}

func (s *Server) deleteGlossaryTerm(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM glossary_terms WHERE id=$1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errGlossaryTermNotFound
	}
	return nil
}
