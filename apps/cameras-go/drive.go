package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

func getOauthConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"https://www.googleapis.com/auth/drive.file"},
		Endpoint:     google.Endpoint,
	}
}

// DriveClient encapsula o acesso autenticado ao Google Drive.
type DriveClient struct {
	client *http.Client
}

// NewDriveClient constrói o cliente com o refresh token vindo do banco.
func NewDriveClient(ctx context.Context, config *oauth2.Config, refreshToken string) *DriveClient {
	token := &oauth2.Token{
		RefreshToken: refreshToken,
	}
	// O oauth2.Config.Client usa o token_source interno que faz o refresh automático
	client := config.Client(ctx, token)
	return &DriveClient{client: client}
}

// UploadResumable faz o upload de um arquivo local para o Google Drive de forma resumível.
func (d *DriveClient) UploadFile(ctx context.Context, filePath, fileName, mimeType string) (string, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return "", err
	}
	fileSize := fileInfo.Size()

	// 1. Inicia a sessão resumível
	meta := map[string]string{
		"name": fileName,
	}
	metaBytes, _ := json.Marshal(meta)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable", bytes.NewReader(metaBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", mimeType)
	req.Header.Set("X-Upload-Content-Length", fmt.Sprintf("%d", fileSize))

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("falha ao iniciar sessão de upload: %d - %s", resp.StatusCode, string(b))
	}

	sessionURI := resp.Header.Get("Location")
	if sessionURI == "" {
		return "", errors.New("location header ausente na resposta de início de upload")
	}

	// 2. Sobe o arquivo na URI de sessão
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	putReq, err := http.NewRequestWithContext(ctx, "PUT", sessionURI, f)
	if err != nil {
		return "", err
	}
	putReq.ContentLength = fileSize

	putResp, err := d.client.Do(putReq)
	if err != nil {
		return "", err
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusOK && putResp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(putResp.Body)
		return "", fmt.Errorf("falha no upload do arquivo: %d - %s", putResp.StatusCode, string(b))
	}

	var result struct {
		Id string `json:"id"`
	}
	if err := json.NewDecoder(putResp.Body).Decode(&result); err != nil {
		return "", err
	}

	slog.Info("Arquivo enviado pro Drive", "id", result.Id, "name", fileName)
	return result.Id, nil
}
