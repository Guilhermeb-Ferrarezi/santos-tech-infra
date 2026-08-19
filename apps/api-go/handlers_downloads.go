package main

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santos-tech/auth/db"
)

// maxDownloadPresignBytes é o teto do que o Santos Hub aceita catalogar como
// "file" — instaladores reais (ex.: build de app interno) costumam ficar bem
// abaixo disso; 2GB cobre qualquer caso razoável sem deixar a rota virar canal
// pra upload de qualquer coisa.
const maxDownloadPresignBytes = 2 << 30

// downloadExtContentType são as extensões aceitas no catálogo (instaladores e
// scripts distribuídos pros PCs da empresa) e o Content-Type gravado no R2 —
// não há sniff de magic-bytes aqui (ao contrário de avatar/model3d): o upload
// é sempre presigned (bytes vão direto pro R2, nunca passam pelo backend) e a
// rota é admin-only, então confiamos na extensão declarada, como já se faz em
// handleVideoPresign. O Content-Type daqui é assinado na URL presigned, então o
// cliente não consegue trocá-lo por text/html na hora do PUT.
var downloadExtContentType = map[string]string{
	"exe":      "application/vnd.microsoft.portable-executable",
	"msi":      "application/x-msi",
	"zip":      "application/zip",
	"7z":       "application/x-7z-compressed",
	"rar":      "application/vnd.rar",
	"dmg":      "application/x-apple-diskimage",
	"pkg":      "application/x-newton-compatible-pkg",
	"deb":      "application/vnd.debian.binary-package",
	"rpm":      "application/x-rpm",
	"appimage": "application/x-executable",
	// ps1/bat/cmd como application/octet-stream (não text/plain): o navegador
	// renderiza text/plain inline na aba em vez de baixar — o objetivo aqui é
	// sempre o download do arquivo, nunca mostrar o script na tela.
	"sh":  "application/x-sh",
	"ps1": "application/octet-stream",
	"bat": "application/octet-stream",
	"cmd": "application/octet-stream",
	"tar": "application/x-tar",
	"gz":  "application/gzip",
	"apk": "application/vnd.android.package-archive",
}

type downloadPresignRequest struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// POST /auth/admin/downloads/presign
func (s *Server) handleDownloadsPresign(w http.ResponseWriter, r *http.Request) {
	if s.r2 == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "UPLOAD_DISABLED", "upload não configurado (R2)"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body downloadPresignRequest
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(body.Filename), "."))
	contentType, ok := downloadExtContentType[ext]
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "extensão não suportada (use exe, msi, zip, 7z, rar, dmg, pkg, deb, rpm, appimage, sh, ps1, bat, cmd, tar, gz ou apk)"))
		return
	}
	if body.Size <= 0 || body.Size > maxDownloadPresignBytes {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "tamanho inválido (máx 2GB)"))
		return
	}

	key := "downloads/" + randomToken(12) + "." + ext
	writeJSON(w, http.StatusOK, map[string]any{
		// contentType é fixado pelo servidor e vai assinado na URL: o PUT precisa
		// mandar exatamente este valor, senão o R2 devolve 403.
		"uploadUrl":   s.r2.PresignPut(key, contentType, 30*time.Minute),
		"objectKey":   key,
		"contentType": contentType,
	})
}

type createDownloadRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Version     string `json:"version"`
	Kind        string `json:"kind"`
	ObjectKey   string `json:"objectKey"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	ExternalUrl string `json:"externalUrl"`
	ImageUrl    string `json:"imageUrl"`
}

// POST /auth/admin/downloads — grava a linha do catálogo. Pra kind="file" o
// arquivo já deve ter sido enviado direto ao R2 via handleDownloadsPresign
// (esta rota só registra o objectKey); pra kind="link" não há upload nenhum.
func (s *Server) handleCreateDownload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body createDownloadRequest
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Category = strings.TrimSpace(body.Category)
	body.Version = strings.TrimSpace(body.Version)
	if body.Name == "" || len(body.Name) > 120 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome é obrigatório (máx 120 caracteres)"))
		return
	}
	if len(body.Description) > 2000 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "descrição deve ter no máximo 2000 caracteres"))
		return
	}
	if len(body.Category) > 60 || len(body.Version) > 40 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "categoria (máx 60) ou versão (máx 40) grande demais"))
		return
	}
	body.ImageUrl = strings.TrimSpace(body.ImageUrl)
	if body.ImageUrl != "" && !strings.HasPrefix(body.ImageUrl, "http://") && !strings.HasPrefix(body.ImageUrl, "https://") {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "imageUrl deve começar com http:// ou https://"))
		return
	}
	if len(body.ImageUrl) > 1000 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "imageUrl grande demais"))
		return
	}

	params := db.CreateDownloadParams{
		Name:        body.Name,
		Description: body.Description,
		Category:    body.Category,
		Version:     body.Version,
		Kind:        body.Kind,
		UploadedBy:  pgtype.Int4{Int32: int32(userIDFrom(r)), Valid: true},
		ImageUrl:    pgtype.Text{String: body.ImageUrl, Valid: body.ImageUrl != ""},
	}

	switch body.Kind {
	case "file":
		if !strings.HasPrefix(body.ObjectKey, "downloads/") {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "objectKey inválido — peça um upload via /auth/admin/downloads/presign"))
			return
		}
		if body.Filename == "" || body.ContentType == "" || body.SizeBytes <= 0 {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "filename, contentType e sizeBytes são obrigatórios pra kind=file"))
			return
		}
		params.ObjectKey = pgtype.Text{String: body.ObjectKey, Valid: true}
		params.Filename = body.Filename
		params.ContentType = body.ContentType
		params.SizeBytes = pgtype.Int8{Int64: body.SizeBytes, Valid: true}
	case "link":
		body.ExternalUrl = strings.TrimSpace(body.ExternalUrl)
		if !strings.HasPrefix(body.ExternalUrl, "http://") && !strings.HasPrefix(body.ExternalUrl, "https://") {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "externalUrl deve começar com http:// ou https://"))
			return
		}
		params.ExternalUrl = pgtype.Text{String: body.ExternalUrl, Valid: true}
	default:
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "kind deve ser 'file' ou 'link'"))
		return
	}

	rec, err := s.q.CreateDownload(r.Context(), params)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": s.downloadAdminJSON(&rec)})
}

// GET /auth/admin/downloads
func (s *Server) handleListDownloadsAdmin(w http.ResponseWriter, r *http.Request) {
	items, err := s.q.ListDownloads(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, s.downloadAdminJSON(&it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// PATCH /auth/admin/downloads/{id} — só metadados (nome/descrição/categoria/
// versão/fixado); trocar o arquivo em si é apagar e recadastrar.
func (s *Server) handleUpdateDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	current, err := s.q.GetDownload(r.Context(), id)
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "item não encontrado"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Category    *string `json:"category"`
		Version     *string `json:"version"`
		Pinned      *bool   `json:"pinned"`
		ImageUrl    *string `json:"imageUrl"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}

	params := db.UpdateDownloadParams{
		ID:          id,
		Name:        current.Name,
		Description: current.Description,
		Category:    current.Category,
		Version:     current.Version,
		Pinned:      current.Pinned,
		ImageUrl:    current.ImageUrl,
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" || len(name) > 120 {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome é obrigatório (máx 120 caracteres)"))
			return
		}
		params.Name = name
	}
	if body.Description != nil {
		if len(*body.Description) > 2000 {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "descrição deve ter no máximo 2000 caracteres"))
			return
		}
		params.Description = *body.Description
	}
	if body.Category != nil {
		if len(*body.Category) > 60 {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "categoria deve ter no máximo 60 caracteres"))
			return
		}
		params.Category = strings.TrimSpace(*body.Category)
	}
	if body.Version != nil {
		if len(*body.Version) > 40 {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "versão deve ter no máximo 40 caracteres"))
			return
		}
		params.Version = strings.TrimSpace(*body.Version)
	}
	if body.Pinned != nil {
		params.Pinned = *body.Pinned
	}
	if body.ImageUrl != nil {
		imageURL := strings.TrimSpace(*body.ImageUrl)
		if imageURL != "" && !strings.HasPrefix(imageURL, "http://") && !strings.HasPrefix(imageURL, "https://") {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "imageUrl deve começar com http:// ou https://"))
			return
		}
		if len(imageURL) > 1000 {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "imageUrl grande demais"))
			return
		}
		params.ImageUrl = pgtype.Text{String: imageURL, Valid: imageURL != ""}
	}

	rec, err := s.q.UpdateDownload(r.Context(), params)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": s.downloadAdminJSON(&rec)})
}

// DELETE /auth/admin/downloads/{id}
func (s *Server) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}
	rec, err := s.q.GetDownload(r.Context(), id)
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "item não encontrado"))
		return
	}
	if n, err := s.q.DeleteDownload(r.Context(), id); err != nil || n == 0 {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "item não encontrado"))
		return
	}
	if rec.Kind == "file" && rec.ObjectKey.Valid && s.r2 != nil {
		if err := s.r2.Delete(r.Context(), rec.ObjectKey.String); err != nil {
			writeErr(w, appErr(http.StatusBadGateway, "DELETE_FAILED", "item removido do catálogo, mas falhou ao apagar o arquivo no R2"))
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /public/downloads — catálogo completo, sem login: é o que o Santos Hub
// (app Tauri dos PCs da empresa) consome pra listar o que dá pra baixar.
func (s *Server) handleListPublicDownloads(w http.ResponseWriter, r *http.Request) {
	items, err := s.q.ListDownloads(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, s.downloadPublicJSON(&it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// downloadURL devolve o link efetivo do item — URL pública no R2 (kind=file)
// ou a URL externa cadastrada (kind=link).
func (s *Server) downloadURL(d *db.Download) string {
	if d.Kind == "file" && d.ObjectKey.Valid && s.r2 != nil {
		return s.r2.PublicURL(d.ObjectKey.String)
	}
	if d.ExternalUrl.Valid {
		return d.ExternalUrl.String
	}
	return ""
}

func (s *Server) downloadPublicJSON(d *db.Download) map[string]any {
	m := map[string]any{
		"id":          d.ID,
		"name":        d.Name,
		"description": d.Description,
		"category":    d.Category,
		"version":     d.Version,
		"kind":        d.Kind,
		"url":         s.downloadURL(d),
		"pinned":      d.Pinned,
		"updatedAt":   d.UpdatedAt.Time,
	}
	if d.SizeBytes.Valid {
		m["sizeBytes"] = d.SizeBytes.Int64
	}
	if d.ImageUrl.Valid {
		m["imageUrl"] = d.ImageUrl.String
	}
	return m
}

func (s *Server) downloadAdminJSON(d *db.Download) map[string]any {
	m := s.downloadPublicJSON(d)
	m["filename"] = d.Filename
	m["contentType"] = d.ContentType
	m["createdAt"] = d.CreatedAt.Time
	if d.ObjectKey.Valid {
		m["objectKey"] = d.ObjectKey.String
	}
	if d.UploadedBy.Valid {
		m["uploadedBy"] = d.UploadedBy.Int32
	}
	return m
}
