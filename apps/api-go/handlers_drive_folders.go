package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode"
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
		// Categoria Unicode Cf (caracteres de formatação, ex. U+202E
		// RIGHT-TO-LEFT OVERRIDE) — sem isso dá pra disfarçar a extensão real
		// de um arquivo (ex. fazer "evil<RLO>fdp.exe" aparecer como
		// "evilexe.pdf" no nome baixado/exibido).
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	if strings.TrimSpace(name) == "" {
		return "arquivo"
	}
	return name
}

// inlineSafeContentTypePrefixes: tipos que o navegador sabe renderizar como
// mídia pura (imagem/vídeo/áudio) ou como documento sem executar script no
// contexto do nosso domínio quando abertos INLINE. Qualquer coisa fora disso
// (HTML, SVG — que pode ter <script> embutido e RODA se aberto como documento
// top-level —, texto que algum navegador tentaria re-interpretar, etc.) é
// sempre forçada a `attachment` + `application/octet-stream`, mesmo sem
// `?download=1` na URL. Isso é a defesa em profundidade: a real proteção
// contra um upload que MENTE o Content-Type é o sniff nos bytes reais feito em
// handleUploadDriveFile (o valor salvo no Drive já vem confiável para uploads
// novos); esta allowlist ainda protege arquivos adicionados fora do nosso
// fluxo de upload (direto no Drive, ou enviados antes deste fix).
func isInlineSafeContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if strings.HasPrefix(ct, "image/svg") {
		return false // SVG pode ter <script> — nunca inline, mesmo sendo "image/*"
	}
	switch {
	case strings.HasPrefix(ct, "image/"),
		strings.HasPrefix(ct, "video/"),
		strings.HasPrefix(ct, "audio/"),
		ct == "application/pdf":
		return true
	}
	return false
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

// GET /auth/admin/drive-folders/browse — pastas que a service account já
// enxerga no Drive (compartilhadas com ela), pro admin escolher em vez de
// colar ID/URL na mão.
func (s *Server) handleBrowseDriveFolders(w http.ResponseWriter, r *http.Request) {
	if s.drive == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "DRIVE_DISABLED", "Arquivos (Google Drive) não configurado"))
		return
	}
	folders, err := s.drive.ListSharedFolders(r.Context())
	if err != nil {
		slog.Error("falha ao listar pastas compartilhadas do Drive", "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "LIST_FAILED", "falha ao listar pastas do Drive"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": folders})
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

// GET /drive-folders/{id}/files?parent=<driveFileId> — folderAccessGuard("read")
// já garantiu o acesso à pasta raiz {id}. `parent` (opcional) navega pra uma
// SUBPASTA dentro dela — validado via IsDescendant pra ninguém escapar da
// árvore autorizada colando um ID de pasta arbitrário do Drive.
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

	target := folder.DriveFolderID
	if parent := strings.TrimSpace(r.URL.Query().Get("parent")); parent != "" && parent != folder.DriveFolderID {
		ok, err := s.drive.IsDescendant(r.Context(), parent, folder.DriveFolderID)
		if err != nil {
			slog.Error("falha ao validar ancestralidade de subpasta do Drive", "folder", folder.ID, "err", err)
			writeErr(w, appErr(http.StatusBadGateway, "LIST_FAILED", "falha ao verificar a subpasta"))
			return
		}
		if !ok {
			writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "pasta fora do escopo autorizado"))
			return
		}
		target = parent
	}

	files, err := s.drive.ListFiles(r.Context(), target)
	if err != nil {
		slog.Error("falha ao listar arquivos do Drive", "folder", folder.ID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "LIST_FAILED", "falha ao listar arquivos"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// ensureFileInFolder garante que fileID está DENTRO da árvore de folder —
// impede que alguém com escrita/leitura numa pasta baixe/renomeie/apague um
// arquivo de FORA dela só por adivinhar/saber o ID no Drive (a ACL deste
// dashboard é por pasta raiz, não por ID individual do Drive).
//
// allowRoot controla se o próprio ID da pasta raiz conta como "dentro dela":
// true para leitura (download/thumbnail — inofensivo, o Drive nem serve
// download/thumbnail de um mimeType de pasta), false para qualquer MUTAÇÃO
// (rename/delete) — `IsDescendant` trata folderID==rootID como válido (correto
// pra navegação, onde "abrir a raiz" é um no-op), mas sem essa checagem extra
// alguém com write numa pasta poderia renomear ou mandar pra lixeira a PASTA
// RAIZ INTEIRA só passando o próprio driveFolderId como fileId.
func (s *Server) ensureFileInFolder(ctx context.Context, folder *DriveFolder, fileID string, allowRoot bool) error {
	if !allowRoot && fileID == folder.DriveFolderID {
		return appErr(http.StatusForbidden, "FORBIDDEN", "não é possível modificar a pasta raiz por aqui")
	}
	ok, err := s.drive.IsDescendant(ctx, fileID, folder.DriveFolderID)
	if err != nil {
		return appErr(http.StatusBadGateway, "CHECK_FAILED", "falha ao verificar o arquivo")
	}
	if !ok {
		return appErr(http.StatusForbidden, "FORBIDDEN", "arquivo fora do escopo autorizado")
	}
	return nil
}

// GET /drive-folders/{id}/files/{fileId}/download?download=1 — sempre
// proxeado pelo backend (a service account é quem tem acesso no Drive, não o
// usuário final). Por padrão abre INLINE (o navegador renderiza imagem/vídeo/
// PDF quando sabe); `?download=1` força o download (Content-Disposition:
// attachment). Repassa o header Range pro Drive — dá pra dar seek num vídeo
// sem baixar o arquivo inteiro primeiro.
func (s *Server) handleDownloadDriveFile(w http.ResponseWriter, r *http.Request) {
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
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	if fileID == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo inválido"))
		return
	}
	if err := s.ensureFileInFolder(r.Context(), folder, fileID, true); err != nil {
		writeErr(w, err)
		return
	}
	body, filename, contentType, status, rangeHeaders, err := s.drive.StreamDownload(r.Context(), fileID, r.Header.Get("Range"))
	if err != nil {
		slog.Error("falha ao baixar arquivo do Drive", "fileId", fileID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "DOWNLOAD_FAILED", "falha ao baixar o arquivo"))
		return
	}
	defer body.Close()
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	disposition := "inline"
	if r.URL.Query().Get("download") != "" {
		disposition = "attachment"
	}
	// Nunca serve inline um tipo fora da allowlist de mídia segura (ver
	// isInlineSafeContentType) — mesmo sem `?download=1` — e nunca deixa o
	// Content-Type declarado por fora dessa allowlist chegar ao navegador,
	// pra não abrir brecha de XSS armazenado (upload que mentiu o tipo, ou
	// arquivo adicionado fora do nosso fluxo de upload).
	if !isInlineSafeContentType(contentType) {
		disposition = "attachment"
		contentType = "application/octet-stream"
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition+`; filename="`+sanitizeFilenameForHeader(filename)+`"`)
	for k, v := range rangeHeaders {
		w.Header()[k] = v
	}
	w.WriteHeader(status)
	if _, err := io.Copy(w, body); err != nil {
		slog.Warn("download do Drive interrompido no meio do stream", "fileId", fileID, "err", err)
	}
}

// GET /drive-folders/{id}/files/{fileId}/thumbnail — miniatura pro grid/lista
// (não todo arquivo tem: 404 nesse caso, o frontend cai pro ícone genérico).
// Cache-Control curto: a miniatura do Drive raramente muda, mas não vale a
// pena investir em cache mais sofisticado só pra isso.
func (s *Server) handleDriveFileThumbnail(w http.ResponseWriter, r *http.Request) {
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
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	if fileID == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo inválido"))
		return
	}
	if err := s.ensureFileInFolder(r.Context(), folder, fileID, true); err != nil {
		writeErr(w, err)
		return
	}
	data, contentType, ok, err := s.drive.GetThumbnail(r.Context(), fileID)
	if err != nil {
		slog.Error("falha ao buscar miniatura do Drive", "fileId", fileID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "THUMBNAIL_FAILED", "falha ao buscar miniatura"))
		return
	}
	if !ok {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "sem miniatura pra esse arquivo"))
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// POST /drive-folders/{id}/files?parent=<driveFileId> — folderAccessGuard("write")
// já garantiu o acesso à pasta raiz {id}. `parent` (opcional, mesma validação
// de ancestralidade do GET) envia pra uma SUBPASTA em vez da raiz. Lê o
// multipart via MultipartReader (streaming puro, sem ParseMultipartForm) pra
// nunca bufferizar o arquivo inteiro — cada parte vai direto pro pipe que
// UploadFile lê e envia ao Drive.
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

	target := folder.DriveFolderID
	if parent := strings.TrimSpace(r.URL.Query().Get("parent")); parent != "" && parent != folder.DriveFolderID {
		ok, err := s.drive.IsDescendant(r.Context(), parent, folder.DriveFolderID)
		if err != nil {
			slog.Error("falha ao validar ancestralidade de subpasta do Drive", "folder", folder.ID, "err", err)
			writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao verificar a subpasta"))
			return
		}
		if !ok {
			writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "pasta fora do escopo autorizado"))
			return
		}
		target = parent
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

		var reader io.Reader = part
		if isInlineSafeContentType(contentType) {
			// Só confere contra os bytes reais quando o tipo DECLARADO já cai
			// na allowlist "segura pra inline" (mesma allowlist do download) —
			// é exatamente aí que uma mentira no Content-Type (ex. manda
			// HTML/script mas declara "image/png") escaparia da checagem de
			// handleDownloadDriveFile. Tipos fora dessa categoria (docx, zip,
			// glb…) já são sempre forçados a download lá, então preservam o
			// Content-Type declarado sem essa validação extra (não vale a pena
			// trocar por um sniff genérico e perder rótulo específico à toa).
			peek := make([]byte, 512)
			n, readErr := io.ReadFull(part, peek)
			if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
				part.Close()
				writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "falha ao ler o arquivo"))
				return
			}
			peek = peek[:n]
			if sniffed := http.DetectContentType(peek); !isInlineSafeContentType(sniffed) {
				part.Close()
				writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "o conteúdo do arquivo não corresponde ao tipo declarado"))
				return
			}
			reader = io.MultiReader(bytes.NewReader(peek), part)
		}

		f, uploadErr := s.drive.UploadFile(r.Context(), target, filename, contentType, reader)
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

// PATCH /drive-folders/{id}/files/{fileId} — folderAccessGuard("write") já
// garantiu acesso de escrita à pasta raiz. Renomeia o arquivo/subpasta
// (ensureFileInFolder impede renomear algo fora da árvore autorizada).
func (s *Server) handleRenameDriveFile(w http.ResponseWriter, r *http.Request) {
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
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	if fileID == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo inválido"))
		return
	}
	if err := s.ensureFileInFolder(r.Context(), folder, fileID, false); err != nil {
		writeErr(w, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 255 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome deve ter entre 1 e 255 caracteres"))
		return
	}

	f, err := s.drive.RenameFile(r.Context(), fileID, name)
	if err != nil {
		slog.Error("falha ao renomear arquivo no Drive", "fileId", fileID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "RENAME_FAILED", "falha ao renomear o arquivo"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": f})
}

// DELETE /drive-folders/{id}/files/{fileId} — folderAccessGuard("write") já
// garantiu acesso de escrita à pasta raiz. Move pra lixeira do Drive — NÃO é
// exclusão permanente (mesmo botão "Remover" da UI do próprio Drive).
func (s *Server) handleDeleteDriveFile(w http.ResponseWriter, r *http.Request) {
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
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	if fileID == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo inválido"))
		return
	}
	if err := s.ensureFileInFolder(r.Context(), folder, fileID, false); err != nil {
		writeErr(w, err)
		return
	}

	if err := s.drive.TrashFile(r.Context(), fileID); err != nil {
		slog.Error("falha ao mover arquivo pra lixeira do Drive", "fileId", fileID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "DELETE_FAILED", "falha ao excluir o arquivo"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
