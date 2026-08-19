package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const maxPresignVideoBytes = 500 << 20 // 500 MB — mesmo teto do admin antigo

// presignExpiry: a URL vale só o tempo de o cliente começar o PUT (o R2 valida
// a assinatura ao RECEBER a requisição, não ao terminá-la — um upload longo que
// começou dentro da janela conclui normalmente). Eram 15-30min, tempo de sobra
// pra a URL vazar e ser reusada pra sobrescrever o objeto já catalogado.
const presignExpiry = 2 * time.Minute

// videoExtByType são os formatos de vídeo aceitos pro upload direto (presigned).
// Diferente de uploadExt, aqui não dá pra fazer sniff de magic-bytes (os bytes
// nunca passam pelo backend), mas o content-type NÃO é livre: só os desta
// allow-list passam, e o valor canônico daqui é o que vai assinado na URL —
// então o objeto no R2 nunca pode acabar servido como text/html.
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
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body presignUploadRequest
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	// Allow-list: o content-type efetivo é o da tabela (chave canônica), nunca a
	// string crua do cliente — é ele que vai ser assinado na URL e gravado no R2.
	declared := strings.ToLower(strings.TrimSpace(body.ContentType))
	if i := strings.IndexByte(declared, ';'); i >= 0 { // descarta parâmetros (";codecs=...")
		declared = strings.TrimSpace(declared[:i])
	}
	ext, ok := videoExtByType[declared]
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "formato de vídeo não suportado (use mp4, webm, mov ou mkv)"))
		return
	}
	contentType := declared
	if body.Size <= 0 || body.Size > maxPresignVideoBytes {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "tamanho inválido (máx 500MB)"))
		return
	}

	uid := userIDFrom(r)
	key := fmt.Sprintf("uploads/%d/%s.%s", uid, randomToken(8), ext)
	writeJSON(w, http.StatusOK, map[string]any{
		"uploadUrl": s.r2.PresignPut(key, contentType, presignExpiry),
		"publicUrl": s.r2.PublicURL(key),
		// O PUT precisa mandar exatamente este Content-Type: ele é parte da
		// assinatura (qualquer outro valor devolve 403 do R2).
		"contentType": contentType,
	})
}
