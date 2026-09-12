package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Handlers do Diário de aula (ver portal_diario.go). Staff: GET/PUT do diário
// de uma aula e a fila de aulas particulares pendentes. Aluno: os arquivos do
// diário, servidos do Drive com a matrícula como única autorização.

// handlePortalGetDiary (GET /portal/sessions/{sessionId}/diary) → {diary}.
// diary é null (com 200) quando a aula existe mas ninguém registrou ainda —
// é o estado normal da tela antes do primeiro save; 404 só se a aula não existe.
func (s *Server) handlePortalGetDiary(w http.ResponseWriter, r *http.Request) {
	sessionID, err := portalPathID(r, "sessionId")
	if err != nil {
		writeErr(w, err)
		return
	}
	diary, err := s.portalGetDiary(r.Context(), sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"diary": diary})
}

// handlePortalPutDiary (PUT /portal/sessions/{sessionId}/diary) — cria ou
// substitui o diário da aula (upsert, idempotente). O autor é quem está
// logado: e-mail e nome vêm da sessão, nunca do corpo.
func (s *Server) handlePortalPutDiary(w http.ResponseWriter, r *http.Request) {
	sessionID, err := portalPathID(r, "sessionId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalDiaryInput
	// Teto próprio (256 KiB) em vez do portalBodyJSON (64 KiB): 20 mil
	// caracteres em UTF-8 mais 50 anexos não cabem com folga nos 64 KiB.
	r.Body = http.MaxBytesReader(w, r.Body, portalDiaryBodyMax)
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	// Anexos e vídeo só podem apontar pra dentro da pasta "Diário de aulas".
	if err := s.portalCheckDiaryFiles(r.Context(), in); err != nil {
		writeErr(w, err)
		return
	}
	u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado"))
		return
	}
	diary, err := s.portalUpsertDiary(r.Context(), sessionID, in, u.Email, u.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "diario_registrado", "session", fmt.Sprint(sessionID), map[string]any{
		"attachments": len(diary.Attachments), "hasVideo": diary.VideoURL != nil || diary.VideoDriveFileID != nil,
	})
	// Pós-aula: todo save do diário dispara a geração das práticas pelo
	// Claude (posaula_gerar.go). É colateral — o diário já está salvo, então
	// fila fora do ar não derruba a request; o professor vê o estado no
	// aiStatus e pode pedir de novo pela rota de regenerate.
	if err := s.enqueuePosaulaGerar(r.Context(), sessionID, diary.UpdatedAt); err != nil {
		slog.Warn("posaula: não consegui enfileirar a geração após salvar o diário", "session", sessionID, "err", err)
	} else {
		pending := "pending"
		diary.AiStatus, diary.AiError = &pending, nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"diary": diary})
}

// handlePortalDiaryPending (GET /portal/diary/pending?days=30) → {sessions}.
// A fila de trabalho do professor: aulas particulares já dadas nos últimos
// `days` dias (1..365), com hasDiary pra separar feito de pendente.
func (s *Server) handlePortalDiaryPending(w http.ResponseWriter, r *http.Request) {
	days, err := portalDiaryDaysFrom(r.URL.Query().Get("days"))
	if err != nil {
		writeErr(w, err)
		return
	}
	sessions, err := s.portalDiaryPending(r.Context(), days)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// handlePortalMyDiaryFile (GET /portal/me/diary/files/{fileId}[?download=1])
// — o aluno baixando um anexo ou o vídeo do diário de uma aula dele. Só
// authGuard: a autorização é a MATRÍCULA (o fileId tem que estar num diário
// de uma turma em que o e-mail da sessão está matriculado), não a ACL de
// pasta do /drive-folders — a pasta "Diário de aulas" nem precisa estar
// cadastrada lá. Fora disso é 404 (não 403): não confirma que o id existe.
// O streaming (Range/206, headers, inline vs attachment) é o mesmo do
// download de /drive-folders — ver streamDriveFile.
func (s *Server) handlePortalMyDiaryFile(w http.ResponseWriter, r *http.Request) {
	if s.drive == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "DRIVE_DISABLED", "Arquivos (Google Drive) não configurado"))
		return
	}
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	if fileID == "" || len(fileID) > portalDiaryTextMax {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "arquivo inválido"))
		return
	}
	u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado"))
		return
	}
	ok, err := s.portalMyDiaryFileAllowed(r.Context(), u.Email, fileID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "arquivo não encontrado"))
		return
	}
	s.streamDriveFile(w, r, fileID)
}
