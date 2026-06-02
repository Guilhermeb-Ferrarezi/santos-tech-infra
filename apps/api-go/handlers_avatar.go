package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

const maxAvatarSize = 5 << 20 // 5 MB

// handleAvatarUpload recebe uma imagem (multipart, campo "file"), sobe pro R2 e
// grava a URL pública em users.avatar_url. Requer sessão (authGuard).
func (s *Server) handleAvatarUpload(w http.ResponseWriter, r *http.Request) {
	if s.r2 == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "UPLOAD_DISABLED", "upload não configurado (R2)"))
		return
	}
	uid := userIDFrom(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarSize+4096)
	if err := r.ParseMultipartForm(maxAvatarSize); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo inválido ou grande demais (máx 5MB)"))
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "envie a imagem no campo 'file'"))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxAvatarSize+1))
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "não consegui ler o arquivo"))
		return
	}
	if len(data) == 0 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo vazio"))
		return
	}
	if len(data) > maxAvatarSize {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo grande demais (máx 5MB)"))
		return
	}

	contentType := http.DetectContentType(data)
	ext, ok := imageExt(contentType)
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "formato não suportado (use png, jpeg, webp ou gif)"))
		return
	}

	key := fmt.Sprintf("avatars/%d/%s.%s", uid, randomToken(8), ext)
	url, err := s.r2.Upload(r.Context(), key, contentType, data)
	if err != nil {
		slog.Error("falha no upload R2", "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao enviar o arquivo"))
		return
	}
	if err := s.updateAvatarURL(r.Context(), uid, url); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"avatarUrl": url})
}

// imageExt valida o content-type detectado e devolve a extensão correspondente.
func imageExt(ct string) (string, bool) {
	switch ct {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/webp":
		return "webp", true
	case "image/gif":
		return "gif", true
	default:
		return "", false
	}
}
