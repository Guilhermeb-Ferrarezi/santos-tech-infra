package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GCalRepo guarda quem autorizou o Google Agenda e qual evento corresponde a
// qual aula.

type GCalAccount struct {
	ID           string
	Email        string
	RefreshToken string
	CalendarID   string
}

type GCalRepo struct{ pool *pgxpool.Pool }

func NewGCalRepo(pool *pgxpool.Pool) *GCalRepo { return &GCalRepo{pool: pool} }

// Salvar grava (ou atualiza) a autorização de uma conta.
//
// ON CONFLICT reautoriza a mesma conta sem criar linha nova, e zera o
// last_error — se a pessoa está autorizando de novo, é justamente porque o
// acesso anterior quebrou.
func (r *GCalRepo) Salvar(ctx context.Context, tenantID TenantID, email, refreshToken string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO google_calendar_account (tenant_id, email, refresh_token)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, email) DO UPDATE SET
		  refresh_token = EXCLUDED.refresh_token,
		  active        = true,
		  last_error    = NULL,
		  updated_at    = now()
	`, tenantID, email, refreshToken)
	if err != nil {
		return fmt.Errorf("GCalRepo.Salvar: %w", err)
	}
	return nil
}

// Ativas devolve as contas que devem receber os eventos.
func (r *GCalRepo) Ativas(ctx context.Context, tenantID TenantID) ([]GCalAccount, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, email, refresh_token, calendar_id
		FROM google_calendar_account
		WHERE tenant_id = $1 AND active
		ORDER BY email
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("GCalRepo.Ativas: %w", err)
	}
	defer rows.Close()
	var out []GCalAccount
	for rows.Next() {
		var a GCalAccount
		if err := rows.Scan(&a.ID, &a.Email, &a.RefreshToken, &a.CalendarID); err != nil {
			return nil, fmt.Errorf("GCalRepo.Ativas scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RegistrarErro anota a falha na conta sem desativá-la.
//
// Não desativa de propósito: uma falha de rede não deve tirar a agenda de
// alguém do ar. O campo existe para o painel mostrar "esta conta parou de
// funcionar" em vez de o problema ficar só no log.
func (r *GCalRepo) RegistrarErro(ctx context.Context, accountID, msg string) {
	_, _ = r.pool.Exec(ctx, `
		UPDATE google_calendar_account SET last_error = $2, updated_at = now()
		WHERE id = $1::uuid
	`, accountID, msg)
}

// VincularEvento guarda a correspondência aula ↔ evento, por conta.
func (r *GCalRepo) VincularEvento(ctx context.Context, tenantID TenantID, accountID, notionPageID, eventID string, startsAt any) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO google_calendar_event (tenant_id, account_id, notion_page_id, event_id, starts_at)
		VALUES ($1, $2::uuid, $3, $4, $5)
		ON CONFLICT (tenant_id, account_id, notion_page_id) DO UPDATE SET
		  event_id  = EXCLUDED.event_id,
		  starts_at = EXCLUDED.starts_at
	`, tenantID, accountID, notionPageID, eventID, startsAt)
	if err != nil {
		return fmt.Errorf("GCalRepo.VincularEvento: %w", err)
	}
	return nil
}

// EventosDaAula devolve os eventos criados para uma aula, um por conta — é o
// que permite cancelar ou remarcar nas duas agendas.
func (r *GCalRepo) EventosDaAula(ctx context.Context, tenantID TenantID, notionPageID string) ([]struct {
	Conta   GCalAccount
	EventID string
}, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id::text, c.email, c.refresh_token, c.calendar_id, e.event_id
		FROM google_calendar_event e
		JOIN google_calendar_account c ON c.id = e.account_id
		WHERE e.tenant_id = $1 AND e.notion_page_id = $2 AND c.active
	`, tenantID, notionPageID)
	if err != nil {
		return nil, fmt.Errorf("GCalRepo.EventosDaAula: %w", err)
	}
	defer rows.Close()
	var out []struct {
		Conta   GCalAccount
		EventID string
	}
	for rows.Next() {
		var item struct {
			Conta   GCalAccount
			EventID string
		}
		if err := rows.Scan(&item.Conta.ID, &item.Conta.Email, &item.Conta.RefreshToken,
			&item.Conta.CalendarID, &item.EventID); err != nil {
			return nil, fmt.Errorf("GCalRepo.EventosDaAula scan: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// EsquecerAula apaga os vínculos depois que os eventos foram removidos.
func (r *GCalRepo) EsquecerAula(ctx context.Context, tenantID TenantID, notionPageID string) {
	_, _ = r.pool.Exec(ctx, `
		DELETE FROM google_calendar_event WHERE tenant_id = $1 AND notion_page_id = $2
	`, tenantID, notionPageID)
}
