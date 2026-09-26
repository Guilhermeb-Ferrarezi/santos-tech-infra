package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"golang.org/x/oauth2"
	"santos-tech.com/cameras-go/db"
)

type SettingsResponse struct {
	ID                         int32  `json:"id"`
	StorageQuotaBytes          int64  `json:"storage_quota_bytes"`
	StorageUsageBytes          int64  `json:"storage_usage_bytes"`
	DriveUserEmail             string `json:"drive_user_email"`
	DriveRefreshTokenEncrypted string `json:"drive_refresh_token_encrypted"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.q.GetSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, SettingsResponse{
			StorageQuotaBytes: 4947802324992,
		})
		return
	}

	resp := SettingsResponse{
		ID:                         settings.ID,
		StorageQuotaBytes:          settings.StorageQuotaBytes,
		DriveRefreshTokenEncrypted: settings.DriveRefreshTokenEncrypted,
	}

	if settings.DriveRefreshTokenEncrypted != "" {
		rt, err := decryptSymmetric(settings.DriveRefreshTokenEncrypted, []byte(s.cfg.EncryptionKey))
		if err == nil {
			cfg := getOauthConfig(
				os.Getenv("GOOGLE_CLIENT_ID"),
				os.Getenv("GOOGLE_CLIENT_SECRET"),
				os.Getenv("OAUTH_REDIRECT_URL"),
			)
			driveClient := NewDriveClient(r.Context(), cfg, rt)
			about, err := driveClient.GetAbout(r.Context())
			if err == nil {
				resp.DriveUserEmail = about.UserEmail
				if about.LimitBytes > 0 {
					resp.StorageQuotaBytes = about.LimitBytes
				}
				resp.StorageUsageBytes = about.UsageBytes
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleOAuthUrl(w http.ResponseWriter, r *http.Request) {
	cfg := getOauthConfig(
		os.Getenv("GOOGLE_CLIENT_ID"),
		os.Getenv("GOOGLE_CLIENT_SECRET"),
		os.Getenv("OAUTH_REDIRECT_URL"),
	)

	// Offline access para receber o refresh_token, prompt consent para forçar a tela.
	authURL := cfg.AuthCodeURL("state", oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL})
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	dashURL := os.Getenv("DASHBOARD_URL")
	if dashURL == "" {
		dashURL = "https://santos-tech.com/dashboard/admin/seguranca/configuracoes"
	}

	redirectError := func(msg string) {
		http.Redirect(w, r, dashURL+"?error="+url.QueryEscape(msg), http.StatusFound)
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		redirectError("Código de autorização ausente")
		return
	}

	cfg := getOauthConfig(
		os.Getenv("GOOGLE_CLIENT_ID"),
		os.Getenv("GOOGLE_CLIENT_SECRET"),
		os.Getenv("OAUTH_REDIRECT_URL"),
	)

	token, err := cfg.Exchange(r.Context(), code)
	if err != nil {
		redirectError("Falha ao trocar código do Google OAuth")
		return
	}

	if token.RefreshToken == "" {
		redirectError("Nenhum refresh token recebido. Revogue a permissão no Google e tente novamente.")
		return
	}

	encToken, err := encryptSymmetric(token.RefreshToken, []byte(s.cfg.EncryptionKey))
	if err != nil {
		redirectError("Erro ao criptografar token")
		return
	}

	// Recupera settings atual
	current, _ := s.q.GetSettings(r.Context())
	var quota int64 = 4947802324992 // 4.5 TB default
	if current.StorageQuotaBytes > 0 {
		quota = current.StorageQuotaBytes
	}

	// Tenta buscar a cota real do Google Drive
	client := cfg.Client(r.Context(), token)
	resp, reqErr := client.Get("https://www.googleapis.com/drive/v3/about?fields=storageQuota")
	if reqErr == nil && resp.StatusCode == http.StatusOK {
		var aboutResp struct {
			StorageQuota struct {
				Limit string `json:"limit"`
				Usage string `json:"usage"`
			} `json:"storageQuota"`
		}
		if parseErr := json.NewDecoder(resp.Body).Decode(&aboutResp); parseErr == nil {
			if limit, parseErr2 := strconv.ParseInt(aboutResp.StorageQuota.Limit, 10, 64); parseErr2 == nil && limit > 0 {
				quota = limit
			}
		}
		resp.Body.Close()
	}

	_, err = s.q.UpsertSettings(context.Background(), db.UpsertSettingsParams{
		StorageQuotaBytes:          quota,
		DriveRefreshTokenEncrypted: encToken,
	})
	if err != nil {
		redirectError("Erro ao salvar configurações no banco de dados")
		return
	}

	http.Redirect(w, r, dashURL+"?connected=true", http.StatusFound)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StorageQuotaBytes int64 `json:"storage_quota_bytes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.StorageQuotaBytes <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_json", "Cota de armazenamento inválida")
		return
	}

	current, _ := s.q.GetSettings(r.Context())
	updated, err := s.q.UpsertSettings(r.Context(), db.UpsertSettingsParams{
		StorageQuotaBytes:          req.StorageQuotaBytes,
		DriveRefreshTokenEncrypted: current.DriveRefreshTokenEncrypted,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, updated)
}
