package main

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santos-tech/auth/db"
)

// GET /auth/admin/ip-bans
func (s *Server) handleListIPBans(w http.ResponseWriter, r *http.Request) {
	bans, err := s.q.ListActiveBans(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(bans))
	for _, b := range bans {
		out = append(out, ipBanJSON(&b))
	}
	writeJSON(w, http.StatusOK, map[string]any{"bans": out})
}

// POST /auth/admin/ip-bans
func (s *Server) handleCreateIPBan(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		IP        string  `json:"ip"`
		Reason    string  `json:"reason"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_BODY", "corpo inválido"))
		return
	}
	if net.ParseIP(body.IP) == nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_IP", "endereço IP inválido"))
		return
	}

	var expiresAt pgtype.Timestamptz
	if body.ExpiresAt != nil && *body.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			writeErr(w, appErr(http.StatusBadRequest, "INVALID_EXPIRES_AT", "formato inválido; use RFC3339"))
			return
		}
		if t.Before(time.Now()) {
			writeErr(w, appErr(http.StatusBadRequest, "INVALID_EXPIRES_AT", "data de expiração já passou"))
			return
		}
		expiresAt = pgtype.Timestamptz{Time: t, Valid: true}
	}

	var reason *string
	if body.Reason != "" {
		reason = &body.Reason
	}

	uid := int32(userIDFrom(r))
	ban, err := s.q.UpsertIPBan(r.Context(), db.UpsertIPBanParams{
		Ip:        body.IP,
		Reason:    reason,
		BannedBy:  pgtype.Int4{Int32: uid, Valid: true},
		ExpiresAt: expiresAt,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	// Sincroniza imediatamente no Redis para efeito instantâneo.
	redisKey := "global:ip-ban:" + ban.Ip
	if ban.ExpiresAt.Valid {
		ttl := time.Until(ban.ExpiresAt.Time)
		if ttl > 0 {
			if err := s.rdb.Set(r.Context(), redisKey, "1", ttl).Err(); err != nil {
				slog.Warn("ip_ban_create: falha ao sincronizar ban no Redis; ban só ativo após restart", "ip", ban.Ip, "err", err)
			}
		}
	} else {
		if err := s.rdb.Set(r.Context(), redisKey, "1", 0).Err(); err != nil {
			slog.Warn("ip_ban_create: falha ao sincronizar ban no Redis; ban só ativo após restart", "ip", ban.Ip, "err", err)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{"ban": ipBanJSON(&ban)})
}

// DELETE /auth/admin/ip-bans/{id}
func (s *Server) handleDeleteIPBan(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}

	ban, err := s.q.GetIPBan(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "ban não encontrado"))
		} else {
			writeErr(w, err)
		}
		return
	}

	n, err := s.q.DeleteIPBan(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n == 0 {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "ban não encontrado"))
		return
	}

	if err := s.rdb.Del(r.Context(), "global:ip-ban:"+ban.Ip).Err(); err != nil {
		slog.Warn("ip_ban_delete: falha ao remover ban do Redis; IP pode permanecer bloqueado até restart", "ip", ban.Ip, "err", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

func ipBanJSON(b *db.IpBan) map[string]any {
	m := map[string]any{
		"id":        b.ID,
		"ip":        b.Ip,
		"reason":    b.Reason,
		"createdAt": b.CreatedAt.Time,
	}
	if b.BannedBy.Valid {
		m["bannedBy"] = b.BannedBy.Int32
	}
	if b.ExpiresAt.Valid {
		m["expiresAt"] = b.ExpiresAt.Time
	}
	return m
}
