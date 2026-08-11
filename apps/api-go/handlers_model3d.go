package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santos-tech/auth/db"
)

const maxModel3DSize = 50 << 20 // 50 MB

// model3DContentType é o Content-Type gravado no R2 por extensão — não há um MIME
// oficial universal para a maioria destes formatos; usamos os mais aceitos na prática.
var model3DContentType = map[string]string{
	"glb":   "model/gltf-binary",
	"gltf":  "model/gltf+json",
	"stl":   "model/stl",
	"obj":   "model/obj",
	"fbx":   "application/octet-stream",
	"blend": "application/octet-stream",
}

// validateModel3DContent confere se os bytes batem com a extensão declarada no
// nome do arquivo (evita, por exemplo, um .exe renomeado para .glb). Devolve a
// extensão normalizada e o content-type a gravar, ou ok=false se não bater.
func validateModel3DContent(data []byte, declaredExt string) (ext, contentType string, ok bool) {
	ext = strings.ToLower(declaredExt)
	ct, known := model3DContentType[ext]
	if !known {
		return "", "", false
	}
	var valid bool
	switch ext {
	case "glb":
		valid = isGLB(data)
	case "gltf":
		valid = isGLTF(data)
	case "stl":
		valid = isSTL(data)
	case "obj":
		valid = isOBJ(data)
	case "fbx":
		valid = isFBX(data)
	case "blend":
		valid = isBlend(data)
	}
	if !valid {
		return "", "", false
	}
	return ext, ct, true
}

// isGLB: binário glTF — magic "glTF" (0x676C5446) nos primeiros 4 bytes, seguido
// de versão (uint32) e tamanho total (uint32) consistente com len(data).
func isGLB(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	if string(data[0:4]) != "glTF" {
		return false
	}
	totalLen := binary.LittleEndian.Uint32(data[8:12])
	return uint64(totalLen) == uint64(len(data))
}

// isGLTF: JSON válido com a chave obrigatória "asset".
func isGLTF(data []byte) bool {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return false
	}
	_, hasAsset := doc["asset"]
	return hasAsset
}

// isSTL: binário (header 80 bytes + uint32 de contagem de triângulos + N*50 bytes,
// tamanho total consistente) ou ASCII começando com "solid".
func isSTL(data []byte) bool {
	if len(data) >= 84 {
		count := binary.LittleEndian.Uint32(data[80:84])
		if uint64(84)+uint64(count)*50 == uint64(len(data)) {
			return true
		}
	}
	return bytes.HasPrefix(bytes.TrimSpace(data), []byte("solid"))
}

// isOBJ: texto com pelo menos uma linha de vértice ("v ") e uma de face ("f ").
func isOBJ(data []byte) bool {
	if len(data) == 0 || len(data) > 0 && !isLikelyText(data) {
		return false
	}
	hasVertex, hasFace := false, false
	for _, line := range bytes.Split(data, []byte("\n")) {
		l := bytes.TrimSpace(line)
		switch {
		case bytes.HasPrefix(l, []byte("v ")):
			hasVertex = true
		case bytes.HasPrefix(l, []byte("f ")):
			hasFace = true
		}
	}
	return hasVertex && hasFace
}

// isFBX: binário ("Kaydara FBX Binary" no header) ou ASCII (começa com "; FBX").
func isFBX(data []byte) bool {
	if bytes.HasPrefix(data, []byte("Kaydara FBX Binary")) {
		return true
	}
	return bytes.HasPrefix(bytes.TrimSpace(data), []byte("; FBX"))
}

// isBlend: ASCII "BLENDER" no header, ou magic gzip (Blender salva .blend
// comprimido opcionalmente) — não vale a pena descomprimir só para validar.
func isBlend(data []byte) bool {
	if bytes.HasPrefix(data, []byte("BLENDER")) {
		return true
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return true // gzip (compressão padrão até o Blender 2.9x)
	}
	return len(data) >= 4 && data[0] == 0x28 && data[1] == 0xb5 && data[2] == 0x2f && data[3] == 0xfd // zstd (padrão desde o Blender 3.0)
}

// isLikelyText reporta se data não contém byte nulo nos primeiros 512 bytes —
// heurística simples para distinguir texto (OBJ) de binário.
func isLikelyText(data []byte) bool {
	n := len(data)
	if n > 512 {
		n = 512
	}
	return !bytes.ContainsRune(data[:n], 0)
}

// POST /auth/admin/models3d
func (s *Server) handleUploadModel3D(w http.ResponseWriter, r *http.Request) {
	if s.r2 == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "UPLOAD_DISABLED", "upload não configurado (R2)"))
		return
	}
	uid := userIDFrom(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxModel3DSize+4096)
	if err := r.ParseMultipartForm(maxModel3DSize); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo inválido ou grande demais (máx 50MB)"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "envie o modelo no campo 'file'"))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxModel3DSize+1))
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "não consegui ler o arquivo"))
		return
	}
	if len(data) == 0 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo vazio"))
		return
	}
	if len(data) > maxModel3DSize {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo grande demais (máx 50MB)"))
		return
	}

	declaredExt := strings.TrimPrefix(strings.ToLower(filepath.Ext(header.Filename)), ".")
	ext, contentType, ok := validateModel3DContent(data, declaredExt)
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "formato não suportado ou conteúdo não bate com a extensão (use glb, gltf, stl, obj, fbx ou blend)"))
		return
	}

	folder := strings.TrimSpace(r.FormValue("folder"))
	if len(folder) > 60 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome da pasta deve ter no máximo 60 caracteres"))
		return
	}

	key := fmt.Sprintf("models3d/%d/%s.%s", uid, randomToken(8), ext)
	if _, err := s.r2.Upload(r.Context(), key, contentType, data); err != nil {
		slog.Error("falha no upload R2 (model3d)", "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao enviar o arquivo"))
		return
	}

	rec, err := s.q.CreateModel3DFile(r.Context(), db.CreateModel3DFileParams{
		Filename:    header.Filename,
		ObjectKey:   key,
		Ext:         ext,
		ContentType: contentType,
		SizeBytes:   int64(len(data)),
		UploadedBy:  pgtype.Int4{Int32: int32(uid), Valid: true},
		Folder:      folder,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"file": s.model3DFileJSON(&rec)})
}

// GET /auth/admin/models3d
func (s *Server) handleListModel3D(w http.ResponseWriter, r *http.Request) {
	files, err := s.q.ListModel3DFiles(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		out = append(out, s.model3DFileJSON(&f))
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out})
}

// PATCH /auth/admin/models3d/{id}
func (s *Server) handlePatchModel3D(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}

	var body struct {
		Filename *string `json:"filename"`
		Folder   *string `json:"folder"`
		Pinned   *bool   `json:"pinned"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if body.Filename != nil && strings.TrimSpace(*body.Filename) == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome não pode ficar vazio"))
		return
	}
	if body.Filename != nil && len(*body.Filename) > 255 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome deve ter no máximo 255 caracteres"))
		return
	}
	if body.Folder != nil && len(*body.Folder) > 60 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome da pasta deve ter no máximo 60 caracteres"))
		return
	}

	current, err := s.q.GetModel3DFile(r.Context(), id)
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "modelo não encontrado"))
		return
	}

	params := db.UpdateModel3DFileParams{
		ID:       id,
		Filename: current.Filename,
		Folder:   current.Folder,
		Pinned:   current.Pinned,
	}
	if body.Filename != nil {
		params.Filename = strings.TrimSpace(*body.Filename)
	}
	if body.Folder != nil {
		params.Folder = strings.TrimSpace(*body.Folder)
	}
	if body.Pinned != nil {
		params.Pinned = *body.Pinned
	}

	rec, err := s.q.UpdateModel3DFile(r.Context(), params)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": s.model3DFileJSON(&rec)})
}

// DELETE /auth/admin/models3d/{id}
func (s *Server) handleDeleteModel3D(w http.ResponseWriter, r *http.Request) {
	if s.r2 == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "UPLOAD_DISABLED", "upload não configurado (R2)"))
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_ID", "id inválido"))
		return
	}

	rec, err := s.q.GetModel3DFile(r.Context(), id)
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "modelo não encontrado"))
		return
	}

	if n, err := s.q.DeleteModel3DFile(r.Context(), id); err != nil || n == 0 {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "modelo não encontrado"))
		return
	}

	if err := s.r2.Delete(r.Context(), rec.ObjectKey); err != nil {
		slog.Error("falha ao apagar objeto no R2 (model3d)", "key", rec.ObjectKey, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "DELETE_FAILED", "arquivo removido do banco, mas falhou ao apagar no R2"))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) model3DFileJSON(f *db.Model3dFile) map[string]any {
	m := map[string]any{
		"id":          f.ID,
		"filename":    f.Filename,
		"ext":         f.Ext,
		"contentType": f.ContentType,
		"sizeBytes":   f.SizeBytes,
		"url":         s.r2.PublicURL(f.ObjectKey),
		"createdAt":   f.CreatedAt.Time,
		"folder":      f.Folder,
		"pinned":      f.Pinned,
	}
	if f.UploadedBy.Valid {
		m["uploadedBy"] = f.UploadedBy.Int32
	}
	return m
}
