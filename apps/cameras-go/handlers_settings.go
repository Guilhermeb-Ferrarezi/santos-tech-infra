package main

import (
	"context"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"santos-tech.com/cameras-go/db"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.q.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Configurações não encontradas")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleOAuthUrl(w http.ResponseWriter, r *http.Request) {
	cfg := getOauthConfig(
		os.Getenv("GOOGLE_CLIENT_ID"),
		os.Getenv("GOOGLE_CLIENT_SECRET"),
		os.Getenv("OAUTH_REDIRECT_URL"),
	)

	// Offline access para receber o refresh_token, prompt consent para forçar a tela.
	url := cfg.AuthCodeURL("state", oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing_code", "Código ausente")
		return
	}

	cfg := getOauthConfig(
		os.Getenv("GOOGLE_CLIENT_ID"),
		os.Getenv("GOOGLE_CLIENT_SECRET"),
		os.Getenv("OAUTH_REDIRECT_URL"),
	)

	token, err := cfg.Exchange(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "exchange_failed", "Falha ao trocar código")
		return
	}

	if token.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "no_refresh_token", "Nenhum refresh token recebido. Tente revogar o acesso no Google e tentar novamente.")
		return
	}

	encToken, err := encryptSymmetric(token.RefreshToken, []byte(s.cfg.EncryptionKey))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "crypto_error", "Erro ao criptografar token")
		return
	}

	// Recupera settings atual pra manter a cota (Upsert atualiza tudo)
	current, _ := s.q.GetSettings(r.Context())
	var quota int64 = 4947802324992 // 4.5 TB default
	if current.StorageQuotaBytes > 0 {
		quota = current.StorageQuotaBytes
	}

	_, err = s.q.UpsertSettings(context.Background(), db.UpsertSettingsParams{
		StorageQuotaBytes:          quota,
		DriveRefreshTokenEncrypted: encToken,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true, "message":"Drive conectado com sucesso"}`))
}
