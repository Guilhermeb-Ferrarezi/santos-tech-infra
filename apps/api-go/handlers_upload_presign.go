package main

import (
	"fmt"
	"net/http"
	"time"
)

const maxPresignVideoBytes = 500 << 20 // 500 MB — mesmo teto do admin antigo

// videoExtByType são os formatos de vídeo aceitos pro upload direto (presigned).
// Diferente de uploadExt, aqui confiamos no content-type declarado pelo cliente —
// os bytes nunca passam pelo backend, então não há como fazer sniff de magic-bytes.
var videoExtByType = map[string]string{
	"video/mp4":        "mp4",
	"video/webm":       "webm",
	"video/quicktime":  "mov",
	"video/x-matroska": "mkv",
}

type presignUploadRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

// handleVideoPresign gera uma URL PUT pré-assinada pro R2 pra upload direto de um
// arquivo de vídeo — o cliente sobe os bytes direto pro storage (PUT na uploadUrl),
// sem passar pelo backend. Requer sessão (authGuard).
func (s *Server) handleVideoPresign(w http.ResponseWriter, r *http.Request) {
	if s.r2 == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "UPLOAD_DISABLED", "upload não configurado (R2)"))
		return
	}
	var body presignUploadRequest
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	ext, ok := videoExtByType[body.ContentType]
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "formato de vídeo não suportado (use mp4, webm, mov ou mkv)"))
		return
	}
	if body.Size <= 0 || body.Size > maxPresignVideoBytes {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "tamanho inválido (máx 500MB)"))
		return
	}

	uid := userIDFrom(r)
	key := fmt.Sprintf("uploads/%d/%s.%s", uid, randomToken(8), ext)
	writeJSON(w, http.StatusOK, map[string]any{
		"uploadUrl": s.r2.PresignPut(key, 15*time.Minute),
		"publicUrl": s.r2.PublicURL(key),
	})
}
