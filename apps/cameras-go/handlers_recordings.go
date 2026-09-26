package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"santos-tech.com/cameras-go/db"
)

func (s *Server) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var camID pgtype.UUID
	if err := camID.Scan(idStr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID da câmera inválido")
		return
	}

	dateStr := r.URL.Query().Get("date") // Formato YYYY-MM-DD
	if dateStr == "" {
		writeError(w, http.StatusBadRequest, "missing_date", "Parâmetro date é obrigatório (YYYY-MM-DD)")
		return
	}

	start, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_date", "Formato de data inválido. Use YYYY-MM-DD")
		return
	}
	end := start.AddDate(0, 0, 1)

	recs, err := s.q.ListRecordings(r.Context(), db.ListRecordingsParams{
		CameraID:    camID,
		StartTime:   pgtype.Timestamptz{Time: start, Valid: true},
		StartTime_2: pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, recs)
}

func (s *Server) handlePatchRecording(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var id pgtype.UUID
	if err := id.Scan(idStr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID da gravação inválido")
		return
	}

	var req struct {
		KeepForever bool `json:"keep_forever"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "JSON inválido")
		return
	}

	if err := s.q.UpdateKeepForever(r.Context(), db.UpdateKeepForeverParams{
		ID:          id,
		KeepForever: req.KeepForever,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var id pgtype.UUID
	if err := id.Scan(idStr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID da gravação inválido")
		return
	}

	rec, err := s.q.GetRecording(r.Context(), id)
	if err == nil && rec.DriveFileID != "" {
		settings, errSettings := s.q.GetSettings(r.Context())
		if errSettings == nil && settings.DriveRefreshTokenEncrypted != "" {
			rt, errDec := decryptSymmetric(settings.DriveRefreshTokenEncrypted, []byte(s.cfg.EncryptionKey))
			if errDec == nil {
				cfg := getOauthConfig(
					os.Getenv("GOOGLE_CLIENT_ID"),
					os.Getenv("GOOGLE_CLIENT_SECRET"),
					os.Getenv("OAUTH_REDIRECT_URL"),
				)
				driveClient := NewDriveClient(r.Context(), cfg, rt)
				_ = driveClient.DeleteFile(r.Context(), rec.DriveFileID)
			}
		}
	}

	if err := s.q.DeleteRecording(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
