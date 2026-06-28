package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Anexos de mídia de um turno (imagens/PDFs vindos do whats-agent via WS).
// Entram no modelo via arquivo no workdir + Read escopado — o CLI em modo -p
// NÃO aceita image blocks no input stream-json (validado em 2026-06-05; ver
// spec 2026-06-05-whats-imagens-design.md no repo do dashboard).

// mediaExt é a fonte única de verdade dos mimetypes aceitos como anexo.
var mediaExt = map[string]string{
	"image/jpeg":      "jpg",
	"image/png":       "png",
	"image/webp":      "webp",
	"image/gif":       "gif",
	"application/pdf": "pdf",
}

const (
	maxAttachments     = 4
	maxAttachmentBytes = 5 << 20 // 5 MB — limite da visão da API
)

type Attachment struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"` // base64 padrão
}

// validateAttachments confere quantidade, mimetype e tamanho (estimado pelo base64).
func validateAttachments(atts []Attachment) error {
	if len(atts) > maxAttachments {
		return fmt.Errorf("máximo de %d anexos por mensagem", maxAttachments)
	}
	for _, a := range atts {
		if _, ok := mediaExt[a.MediaType]; !ok {
			return fmt.Errorf("mimetype não suportado: %s", a.MediaType)
		}
		if len(a.Data)/4*3 > maxAttachmentBytes {
			return fmt.Errorf("anexo excede 5 MB")
		}
	}
	return nil
}

// mediaPromptNote afirma ao modelo que os arquivos existem e que a tool Read está
// liberada para eles — sem a afirmação explícita, o modelo recusa usar a tool
// (gotcha conhecido do gate de terceiros, igual ao do WebSearch).
func mediaPromptNote(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return "[anexos desta mensagem — você TEM a ferramenta Read liberada para estes arquivos; " +
		"leia-os antes de responder]\n" + strings.Join(paths, "\n") + "\n\n"
}

// mediaMarkers gera os marcadores persistidos no transcript (nunca o base64).
func mediaMarkers(atts []Attachment) string {
	var b strings.Builder
	for _, a := range atts {
		if a.MediaType == "application/pdf" {
			b.WriteString(" [pdf]")
		} else {
			b.WriteString(" [imagem]")
		}
	}
	return b.String()
}

// saveAttachments decodifica e grava os anexos em <workdir>/media; devolve os paths
// absolutos. Nome é UUID nosso + extensão derivada do mimetype validado.
// Se atts for vazio, retorna imediatamente sem criar o diretório de mídia.
func saveAttachments(workdir string, atts []Attachment) ([]string, error) {
	if len(atts) == 0 {
		return nil, nil
	}
	dir := filepath.Join(workdir, "media")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(atts))
	for _, a := range atts {
		raw, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return nil, fmt.Errorf("base64 inválido: %w", err)
		}
		if len(raw) > maxAttachmentBytes {
			return nil, fmt.Errorf("anexo excede 5 MB")
		}
		p := filepath.Join(dir, newUUID()+"."+mediaExt[a.MediaType])
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}
