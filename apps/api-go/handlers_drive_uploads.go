package main

// Handlers HTTP do upload resumable (ver drive_resumable.go pro protocolo com
// o Google e o registro da sessão). As três rotas usam folderAccessGuard
// ("write") — mesmo requisito de POST /drive-folders/{id}/files — porque são
// literalmente o mesmo upload, só que fatiado em pedaços.

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// driveResumableMaxChunkBody: teto do corpo aceito num PUT de pedaço.
// chunkSize*2 dá folga ao front (que sempre manda exatamente chunkSize,
// exceto o último pedaço, menor) sem abrir mão de um limite bem abaixo de
// qualquer coisa razoável — MaxBytesReader corta a leitura aqui.
const driveResumableMaxChunkBody = driveResumableChunkSize * 2

// driveResumableChunkAlignment: todo pedaço que não é o último tem que
// começar num múltiplo de 256 KiB — exigência do protocolo resumable do
// Google (o mesmo valor que limita o chunkSize sugerido ao front).
const driveResumableChunkAlignment = 256 << 10

type driveUploadInput struct {
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

func (in driveUploadInput) validate() error {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 255 {
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome deve ter entre 1 e 255 caracteres")
	}
	if in.Size <= 0 || in.Size > maxDriveResumableSize {
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "tamanho do arquivo inválido (máx 16GB)")
	}
	return nil
}

// POST /drive-folders/{id}/uploads?parent=<driveFileId> — folderAccessGuard
// ("write") já garantiu acesso de escrita à raiz {id}. Abre uma sessão de
// upload resumable no Drive pra um arquivo grande demais pro multipart de
// POST /drive-folders/{id}/files (teto 500MB) — devolve um `uploadId` opaco
// que o front usa nos PUTs de pedaço (a URI real da sessão no Google fica só
// no registro, nunca sai daqui — ver comentário no topo de drive_resumable.go).
func (s *Server) handleCreateDriveUpload(w http.ResponseWriter, r *http.Request) {
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
	s.createDriveUpload(w, r, folder)
}

// createDriveUpload é o miolo de handleCreateDriveUpload separado do lookup
// de `folder` no Postgres — mesmo motivo de ensureFileInFolder (drive_data.go)
// receber a pasta já resolvida: os testes deste pacote não sobem Postgres, e
// aqui dá pra exercitar validação/ancestralidade/sessão com só um `*DriveFolder`
// montado na mão + o fakeDriveClient de drive_test.go.
func (s *Server) createDriveUpload(w http.ResponseWriter, r *http.Request, folder *DriveFolder) {
	target := folder.DriveFolderID
	if parent := strings.TrimSpace(r.URL.Query().Get("parent")); parent != "" && parent != folder.DriveFolderID {
		ok, err := s.driveIsDescendantCached(r.Context(), parent, folder.DriveFolderID)
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

	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	var in driveUploadInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	mimeType := strings.TrimSpace(in.MimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	sessionURI, err := s.drive.StartResumableUploadAs(r.Context(), folder.UploadAccount, target, name, mimeType, in.Size)
	if err != nil {
		slog.Error("falha ao abrir sessão de upload resumable no Drive", "folder", folder.ID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao iniciar o envio"))
		return
	}

	uploadID := randomToken(32)
	sess := driveUploadSession{
		SessionURI: sessionURI,
		Account:    folder.UploadAccount,
		FolderID:   folder.ID,
		Size:       in.Size,
		MimeType:   mimeType,
		Name:       name,
		UserID:     userIDFrom(r),
		CreatedAt:  time.Now(),
	}
	if err := s.saveDriveUploadSession(r.Context(), uploadID, sess); err != nil {
		// O Drive já aceitou a sessão (sessionURI acima é uma credencial de
		// escrita REAL) mas não sobra registro nenhum dela em lugar nenhum —
		// fica órfã até expirar sozinha (1 semana no Google). Não dá pra
		// abandoná-la no Drive por um preço razoável (a API não tem um
		// "cancelar sessão resumable" antes do primeiro PUT), então pelo menos
		// loga o bastante (folder + nome + a própria URI) pra investigação
		// manual, já que o cliente só vê um 502 genérico.
		slog.Error("falha ao salvar sessão de upload resumable no Redis: sessão do Drive fica órfã",
			"folder", folder.ID, "name", name, "sessionUri", sessionURI, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao iniciar o envio"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uploadId": uploadID, "chunkSize": driveResumableChunkSize})
}

// driveUploadSessionForRequest resolve e valida a sessão do path {uploadId}
// pra {id}/uploads/*: formato do id, existência no Redis, dono (userId) e
// pasta (folderId) batendo com a requisição atual. As três causas de recusa
// (formato ruim, sessão inexistente, sessão de OUTRO usuário/pasta) respondem
// o MESMO 404 — mesmo espírito de não vazar existência de forgot-password:
// um uploadId de outro usuário não pode se distinguir de um uploadId que
// nunca existiu.
func (s *Server) driveUploadSessionForRequest(w http.ResponseWriter, r *http.Request, folder *DriveFolder) (*driveUploadSession, string, bool) {
	uploadID := r.PathValue("uploadId")
	if !isValidDriveUploadID(uploadID) {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "envio não encontrado"))
		return nil, "", false
	}
	sess, err := s.getDriveUploadSession(r.Context(), uploadID)
	if err != nil {
		slog.Error("falha ao consultar sessão de upload resumable no Redis", "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao consultar o envio"))
		return nil, "", false
	}
	if sess == nil || sess.UserID != userIDFrom(r) || sess.FolderID != folder.ID {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "envio não encontrado"))
		return nil, "", false
	}
	return sess, uploadID, true
}

// PUT /drive-folders/{id}/uploads/{uploadId} — folderAccessGuard("write") já
// garantiu acesso de escrita à raiz {id}. Envia um pedaço do arquivo (header
// Content-Range obrigatório) — devolve {done:false, received} enquanto o
// Google pede mais pedaços (308), ou {done:true, file} no pedaço que fecha o
// upload (200/201).
func (s *Server) handlePutDriveUploadChunk(w http.ResponseWriter, r *http.Request) {
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
	s.putDriveUploadChunk(w, r, folder)
}

// putDriveUploadChunk é o miolo de handlePutDriveUploadChunk, separado do
// lookup de `folder` — mesmo motivo de createDriveUpload acima.
func (s *Server) putDriveUploadChunk(w http.ResponseWriter, r *http.Request, folder *DriveFolder) {
	sess, uploadID, ok := s.driveUploadSessionForRequest(w, r, folder)
	if !ok {
		return
	}

	cr, ok := parseContentRange(r.Header.Get("Content-Range"))
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "Content-Range inválido"))
		return
	}
	if cr.Total != sess.Size {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "tamanho total não confere com o envio"))
		return
	}
	length := cr.End - cr.Start + 1
	if r.ContentLength >= 0 && r.ContentLength != length {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "Content-Length não confere com o Content-Range"))
		return
	}
	if length > driveResumableMaxChunkBody {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "pedaço grande demais"))
		return
	}
	// O ÚLTIMO pedaço pode (e normalmente vai) começar fora do alinhamento —
	// só os pedaços do MEIO precisam começar em múltiplo de 256KiB. end==Total-1
	// identifica o último pedaço sem precisar de outro parâmetro na requisição.
	if cr.Start%driveResumableChunkAlignment != 0 && cr.End != cr.Total-1 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "início do pedaço deve ser múltiplo de 256KiB"))
		return
	}

	if rc := http.NewResponseController(w); rc != nil {
		if err := rc.SetReadDeadline(time.Now().Add(driveUploadReadTimeout)); err != nil {
			slog.Error("falha ao estender read deadline do upload de pedaço", "err", err)
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, length)

	var reader io.Reader = r.Body
	// Primeiro pedaço + tipo declarado (na criação da sessão) cai na allowlist
	// "segura pra inline": mesma defesa em profundidade do multipart (ver
	// handleUploadDriveFile) — sniffa os bytes reais contra o Content-Type
	// declarado, senão o resumable vira uma porta lateral pra subir HTML/script
	// disfarçado de mídia (esses bytes nunca passam pelo sniff do multipart,
	// que é uma rota HTTP totalmente diferente).
	if cr.Start == 0 && isInlineSafeContentType(sess.MimeType) {
		peek := make([]byte, 512)
		n, readErr := io.ReadFull(r.Body, peek)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "falha ao ler o pedaço"))
			return
		}
		peek = peek[:n]
		if sniffed := http.DetectContentType(peek); !isInlineSafeContentType(sniffed) {
			s.deleteDriveUploadSession(uploadID)
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "o conteúdo do arquivo não corresponde ao tipo declarado"))
			return
		}
		reader = io.MultiReader(bytes.NewReader(peek), r.Body)
	}

	result, err := s.drive.PutResumableChunk(r.Context(), sess.Account, sess.SessionURI, cr.Start, cr.End, cr.Total, reader)
	if err != nil {
		if errors.Is(err, errResumableSessionLost) {
			s.deleteDriveUploadSession(uploadID)
			writeErr(w, appErr(http.StatusGone, "UPLOAD_SESSION_LOST", "a sessão de envio expirou, comece de novo"))
			return
		}
		slog.Error("falha ao enviar pedaço pro Drive", "folder", folder.ID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao enviar o pedaço (tente de novo)"))
		return
	}
	if result.Done {
		s.deleteDriveUploadSession(uploadID)
		writeJSON(w, http.StatusCreated, map[string]any{"done": true, "file": result.File})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"done": false, "received": result.Received})
}

// GET /drive-folders/{id}/uploads/{uploadId} — folderAccessGuard("write") já
// garantiu acesso de escrita à raiz {id}. Consulta quanto o Google já recebeu
// SEM mandar nenhum byte novo — é o que o front chama depois de uma queda de
// conexão pra saber de onde retomar os PUTs de pedaço.
func (s *Server) handleGetDriveUploadStatus(w http.ResponseWriter, r *http.Request) {
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
	s.getDriveUploadStatus(w, r, folder)
}

// getDriveUploadStatus é o miolo de handleGetDriveUploadStatus, separado do
// lookup de `folder` — mesmo motivo de createDriveUpload acima.
func (s *Server) getDriveUploadStatus(w http.ResponseWriter, r *http.Request, folder *DriveFolder) {
	sess, uploadID, ok := s.driveUploadSessionForRequest(w, r, folder)
	if !ok {
		return
	}

	result, err := s.drive.GetResumableStatus(r.Context(), sess.Account, sess.SessionURI, sess.Size)
	if err != nil {
		if errors.Is(err, errResumableSessionLost) {
			s.deleteDriveUploadSession(uploadID)
			writeErr(w, appErr(http.StatusGone, "UPLOAD_SESSION_LOST", "a sessão de envio expirou, comece de novo"))
			return
		}
		slog.Error("falha ao consultar status do upload resumable no Drive", "folder", folder.ID, "err", err)
		writeErr(w, appErr(http.StatusBadGateway, "UPLOAD_FAILED", "falha ao consultar o envio"))
		return
	}
	if result.Done {
		s.deleteDriveUploadSession(uploadID)
		writeJSON(w, http.StatusOK, map[string]any{"done": true, "file": result.File})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"done": false, "received": result.Received})
}
