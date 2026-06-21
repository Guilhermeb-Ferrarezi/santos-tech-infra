package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

const maxUploadSize = 5 << 20 // 5 MB

// uploadExt resolve a extensão a partir do content-type detectado para o upload
// genérico de /auth/upload: imagens (png/jpeg/webp/gif) e também PDF. imageExt
// continua restrito a imagens (usado pelo avatar), então PDF é tratado aqui.
func uploadExt(ct string) (string, bool) {
	if ct == "application/pdf" {
		return "pdf", true
	}
	return imageExt(ct)
}

// handleImageUpload recebe um arquivo (multipart, campo "file"), sobe pro R2 e
// devolve a URL pública — sem gravar em lugar nenhum. Genérico: serve para fotos
// de remetente, capas, PDFs entregáveis etc. Requer sessão (authGuard). Espelha
// handleAvatarUpload, mas não toca no perfil do usuário.
func (s *Server) handleImageUpload(w http.ResponseWriter, r *http.Request) {
	if s.r2 == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "UPLOAD_DISABLED", "upload não configurado (R2)"))
		return
	}
	uid := userIDFrom(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+4096)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo inválido ou grande demais (máx 5MB)"))
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "envie a imagem no campo 'file'"))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "não consegui ler o arquivo"))
		return
	}
	if len(data) == 0 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo vazio"))
		return
	}
	if len(data) > maxUploadSize {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo grande demais (máx 5MB)"))
		return
	}

	contentType := http.DetectContentType(data)
	ext, ok := uploadExt(contentType)
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "formato não suportado (use png, jpeg, webp, gif ou pdf)"))
		return
	}

	key := fmt.Sprintf("uploads/%d/%s.%s", uid, randomToken(8), ext)
	url, err := s.r2.Upload(r.Context(), key, contentType, data)
	if err != nil {
		slog.Error("falha no upload R2", "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao enviar o arquivo"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}
