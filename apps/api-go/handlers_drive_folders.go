package main

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

const (
	driveFolderNameMax        = 120
	driveFolderDescriptionMax = 500
	driveFolderIDMax          = 200
	maxDriveUploadSize        = 25 << 20 // 25MB — streamado, nunca bufferizado inteiro em memória
)

// extractDriveFolderID aceita tanto o ID puro quanto uma URL de pasta colada
// pelo admin (https://drive.google.com/drive/folders/<id>?usp=sharing),
// extraindo só o ID — evita o erro comum de colar a URL inteira.
func extractDriveFolderID(raw string) string {
	raw = strings.TrimSpace(raw)
	const marker = "/folders/"
	if idx := strings.Index(raw, marker); idx != -1 {
		rest := raw[idx+len(marker):]
		if q := strings.IndexAny(rest, "?/#"); q != -1 {
			rest = rest[:q]
		}
		if rest != "" {
			return rest
		}
	}
	return raw
}

func sanitizeFilenameForHeader(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, name)
	if strings.TrimSpace(name) == "" {
		return "arquivo"
	}
	return name
}

type driveFolderInput struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	DriveFolderID string `json:"driveFolderId"`
}

func (in driveFolderInput) validate() error {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > driveFolderNameMax {
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome deve ter entre 1 e 120 caracteres")
	}
	if len(in.Description) > driveFolderDescriptionMax {
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "descrição deve ter no máximo 500 caracteres")
	}
	id := extractDriveFolderID(in.DriveFolderID)
	if id == "" || len(id) > driveFolderIDMax {
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "ID da pasta no Drive é obrigatório")
	}
	return nil
}

// GET /auth/admin/drive-folders
func (s *Server) handleListDriveFoldersAdmin(w http.ResponseWriter, r *http.Request) {
	folders, err := s.listDriveFolders(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": folders})
}

// POST /auth/admin/drive-folders
func (s *Server) handleCreateDriveFolder(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in driveFolderInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	folder, err := s.insertDriveFolder(r.Context(),
		strings.TrimSpace(in.Name), strings.TrimSpace(in.Description),
		extractDriveFolderID(in.DriveFolderID), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"folder": folder})
}

// PUT /auth/admin/drive-folders/{id}
func (s *Server) handleUpdateDriveFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in driveFolderInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	folder, err := s.updateDriveFolderRow(r.Context(), id,
		strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), extractDriveFolderID(in.DriveFolderID))
	if err != nil {
		writeErr(w, err)
		return
	}
	if folder == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folder": folder})
}

// DELETE /auth/admin/drive-folders/{id}
func (s *Server) handleDeleteDriveFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
		return
	}
	ok, err := s.deleteDriveFolderRow(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /auth/admin/drive-folders/{id}/access
func (s *Server) handleGetDriveFolderAccess(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
		return
	}
	roles, err := s.listDriveFolderRoleAccess(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	members, err := s.listDriveFolderMemberAccess(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles, "members": members})
}

type driveFolderAccessInput struct {
	Roles   []DriveFolderRoleAccess  `json:"roles"`
	Members []DriveFolderMemberInput `json:"members"`
}

// PUT /auth/admin/drive-folders/{id}/access — substitui o conjunto completo de
// ACL da pasta (cargos + membros individuais); o admin manda o estado final
// desejado, não deltas (mesmo espírito de replaceDriveFolderAccess).
func (s *Server) handleSetDriveFolderAccess(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in driveFolderAccessInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if len(in.Roles) > 100 || len(in.Members) > 500 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "lista de acesso grande demais"))
		return
	}

	seenRoles := map[string]bool{}
	for _, ra := range in.Roles {
		if ra.Access != "read" && ra.Access != "write" {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "access deve ser 'read' ou 'write'"))
			return
		}
		switch ra.RoleKind {
		case "fixed":
			if ra.RoleValue != strconv.Itoa(RoleStudent) && ra.RoleValue != strconv.Itoa(RoleTeacher) {
				writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "cargo fixo inválido"))
				return
			}
		case "custom":
			if !uuidRe.MatchString(ra.RoleValue) {
				writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "cargo personalizado inválido"))
				return
			}
		default:
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "roleKind deve ser 'fixed' ou 'custom'"))
			return
		}
		key := ra.RoleKind + ":" + ra.RoleValue
		if seenRoles[key] {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "cargo duplicado na lista de acesso"))
			return
		}
		seenRoles[key] = true
	}

	seenMembers := map[int64]bool{}
	memberIDs := make([]int64, 0, len(in.Members))
	for _, m := range in.Members {
		if m.Access != "read" && m.Access != "write" {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "access deve ser 'read' ou 'write'"))
			return
		}
		if seenMembers[m.UserID] {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "usuário duplicado na lista de acesso"))
			return
		}
		seenMembers[m.UserID] = true
		memberIDs = append(memberIDs, m.UserID)
	}
	if err := s.validateAssigneeIDs(r.Context(), memberIDs); err != nil {
		writeErr(w, err)
		return
	}

	folder, err := s.getDriveFolder(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if folder == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
		return
	}
	if err := s.replaceDriveFolderAccess(r.Context(), id, in.Roles, in.Members); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /drive-folders/mine — pastas com acesso >= read pro usuário logado.
// Admin vê todas com write (resolvido aqui, sem passar pela ACL — ele sempre
// tem acesso total; ver folderAccessGuard em drive_access.go pro mesmo critério
// aplicado nas rotas de arquivo).
func (s *Server) handleListMyDriveFolders(w http.ResponseWriter, r *http.Request) {
	u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito"))
		return
	}
	if u.Role == RoleAdmin {
		all, err := s.listDriveFolders(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		out := make([]MyDriveFolder, 0, len(all))
		for _, f := range all {
			out = append(out, MyDriveFolder{
				ID: f.ID, Name: f.Name, Description: f.Description, DriveFolderID: f.DriveFolderID,
				Access: "write", CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"folders": out})
		return
	}
	folders, err := s.listDriveFoldersForUser(r.Context(), u.ID, u.Role, u.CustomRoleID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": folders})
}

// GET /drive-folders/{id}/files — folderAccessGuard("read") já garantiu o acesso.
func (s *Server) handleListDriveFolderFiles(w http.ResponseWriter, r *http.Request) {
	if s.drive == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "DRIVE_DISABLED", "Arquivos (Google Drive) não configurado"))
		return
	}
	folder, err := s.getDriveFolder(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if folder == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
		return
	}
	files, err := s.drive.ListFiles(r.Context(), folder.DriveFolderID)
	if err != nil {
		slog.Error("falha ao listar arquivos do Drive", "folder", folder.ID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "LIST_FAILED", "falha ao listar arquivos"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// GET /drive-folders/{id}/files/{fileId}/download — sempre proxeado pelo
// backend (a service account é quem tem acesso no Drive, não o usuário final).
func (s *Server) handleDownloadDriveFile(w http.ResponseWriter, r *http.Request) {
	if s.drive == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "DRIVE_DISABLED", "Arquivos (Google Drive) não configurado"))
		return
	}
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	if fileID == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo inválido"))
		return
	}
	body, filename, contentType, err := s.drive.StreamDownload(r.Context(), fileID)
	if err != nil {
		slog.Error("falha ao baixar arquivo do Drive", "fileId", fileID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "DOWNLOAD_FAILED", "falha ao baixar o arquivo"))
		return
	}
	defer body.Close()
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilenameForHeader(filename)+`"`)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		slog.Warn("download do Drive interrompido no meio do stream", "fileId", fileID, "err", err)
	}
}

// POST /drive-folders/{id}/files — folderAccessGuard("write") já garantiu o
// acesso. Lê o multipart via MultipartReader (streaming puro, sem
// ParseMultipartForm) pra nunca bufferizar o arquivo inteiro — cada parte vai
// direto pro pipe que UploadFile lê e envia ao Drive.
func (s *Server) handleUploadDriveFile(w http.ResponseWriter, r *http.Request) {
	if s.drive == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "DRIVE_DISABLED", "Arquivos (Google Drive) não configurado"))
		return
	}
	folder, err := s.getDriveFolder(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if folder == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
		return
	}
	if r.ContentLength > maxDriveUploadSize+(64<<10) {
		writeErr(w, appErr(http.StatusRequestEntityTooLarge, "VALIDATION_ERROR", "arquivo grande demais (máx 25MB)"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDriveUploadSize+(64<<10))
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "requisição multipart inválida"))
		return
	}

	var uploaded *DriveFile
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "requisição multipart inválida ou arquivo grande demais (máx 25MB)"))
			return
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		filename := strings.TrimSpace(part.FileName())
		if filename == "" {
			part.Close()
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "envie o arquivo no campo 'file'"))
			return
		}
		contentType := part.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		f, uploadErr := s.drive.UploadFile(r.Context(), folder.DriveFolderID, filename, contentType, part)
		part.Close()
		if uploadErr != nil {
			slog.Error("falha no upload pro Drive", "folder", folder.ID, "err", uploadErr)
			writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao enviar o arquivo (verifique o tamanho e tente de novo)"))
			return
		}
		uploaded = &f
		break
	}
	if uploaded == nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "envie o arquivo no campo 'file'"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"file": uploaded})
}
