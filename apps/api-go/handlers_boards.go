package main

// Quadros (Excalidraw) — handlers HTTP. Restritos a admins e professores
// (staffGuard). Quadro inexistente e quadro sem acesso respondem o mesmo 404,
// pra não vazar a existência de quadros alheios.

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// maxBoardScene limita a cena (JSON com imagens base64 embutidas) em 10 MB.
const maxBoardScene = 10 << 20

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var errBoardNotFound = appErr(http.StatusNotFound, "BOARD_NOT_FOUND", "Quadro não encontrado")

// staffGuard exige sessão válida E role de admin ou professor — o público dos
// quadros. Segue o padrão do adminGuard.
func (s *Server) staffGuard(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userByID(r.Context(), userIDFrom(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		if u == nil || (u.Role != RoleAdmin && u.Role != RoleTeacher) {
			writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito a administradores e professores"))
			return
		}
		next(w, r)
	})
}

// boardIDFrom valida o {id} do path como UUID (evita erro de cast no Postgres
// virar 500). UUID malformado é indistinguível de quadro inexistente: 404.
func boardIDFrom(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		return "", errBoardNotFound
	}
	return id, nil
}

// GET /boards — meus quadros + compartilhados comigo (sem cena).
func (s *Server) handleListBoards(w http.ResponseWriter, r *http.Request) {
	boards, err := s.listBoardsForUser(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"boards": boards})
}

// POST /boards — cria um quadro vazio {title}.
func (s *Server) handleCreateBoard(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 200 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Título obrigatório (até 200 caracteres)"))
		return
	}
	b, err := s.insertBoard(r.Context(), userIDFrom(r), in.Title)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"board": b})
}

// GET /boards/{id} — metadados + cena.
func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	id, err := boardIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	b, err := s.boardForUser(r.Context(), id, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if b == nil {
		writeErr(w, errBoardNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"board": b})
}

// PUT /boards/{id} — salva {scene, sceneVersion, title?}. Versão divergente →
// 409 (alguém salvou antes); título só o dono muda.
func (s *Server) handleUpdateBoard(w http.ResponseWriter, r *http.Request) {
	id, err := boardIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBoardScene+4096)
	var in struct {
		Title        *string         `json:"title"`
		Scene        json.RawMessage `json:"scene"`
		SceneVersion *int            `json:"sceneVersion"`
	}
	if err := decodeJSON(r, &in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, appErr(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Quadro grande demais (máx. 10MB)"))
			return
		}
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	// Dois modos: salvar a cena (scene+sceneVersion, com título opcional) ou
	// só renomear (title sem scene — usado pela lista, sem carregar a cena).
	titleOnly := len(in.Scene) == 0 && in.Title != nil
	if !titleOnly && (len(in.Scene) == 0 || in.SceneVersion == nil || *in.SceneVersion < 0) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "scene e sceneVersion são obrigatórios"))
		return
	}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" || len(t) > 200 {
			writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Título inválido (até 200 caracteres)"))
			return
		}
		in.Title = &t
	}

	role, err := s.boardRoleFor(r.Context(), id, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if role == "" {
		writeErr(w, errBoardNotFound)
		return
	}
	if role == "viewer" {
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Você só pode visualizar este quadro"))
		return
	}
	if in.Title != nil && role != "owner" {
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Só o dono pode renomear o quadro"))
		return
	}

	if titleOnly {
		v, err := s.updateBoardTitle(r.Context(), id, *in.Title)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sceneVersion": v})
		return
	}

	v, err := s.updateBoardScene(r.Context(), id, in.Scene, *in.SceneVersion, in.Title)
	if err != nil {
		writeErr(w, err)
		return
	}
	if v == 0 {
		writeErr(w, appErr(http.StatusConflict, "VERSION_CONFLICT", "O quadro foi salvo por outra pessoa — recarregue para continuar"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sceneVersion": v})
}

// DELETE /boards/{id} — só o dono.
func (s *Server) handleDeleteBoard(w http.ResponseWriter, r *http.Request) {
	id, err := boardIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.requireBoardOwner(w, r, id); err != nil {
		return
	}
	if err := s.deleteBoard(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireBoardOwner resolve o papel e escreve o erro se quem pediu não for o
// dono. Devolve erro não-nil sempre que a resposta já foi escrita.
func (s *Server) requireBoardOwner(w http.ResponseWriter, r *http.Request, boardID string) error {
	role, err := s.boardRoleFor(r.Context(), boardID, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return err
	}
	if role == "" {
		writeErr(w, errBoardNotFound)
		return errBoardNotFound
	}
	if role != "owner" {
		err := appErr(http.StatusForbidden, "FORBIDDEN", "Só o dono pode fazer isso")
		writeErr(w, err)
		return err
	}
	return nil
}

// GET /boards/{id}/members — lista membros (só o dono gerencia).
func (s *Server) handleListBoardMembers(w http.ResponseWriter, r *http.Request) {
	id, err := boardIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.requireBoardOwner(w, r, id); err != nil {
		return
	}
	members, err := s.listBoardMembers(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

// POST /boards/{id}/members — adiciona/atualiza membro por email {email, role}.
// Email inexistente e email sem papel de staff respondem o MESMO 404 (não
// revela qual dos dois é o caso).
func (s *Server) handleAddBoardMember(w http.ResponseWriter, r *http.Request) {
	id, err := boardIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !emailRe.MatchString(in.Email) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Email inválido"))
		return
	}
	if in.Role != "viewer" && in.Role != "editor" {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Papel deve ser viewer ou editor"))
		return
	}
	if err := s.requireBoardOwner(w, r, id); err != nil {
		return
	}
	u, err := s.userByEmail(r.Context(), in.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || (u.Role != RoleAdmin && u.Role != RoleTeacher) {
		writeErr(w, appErr(http.StatusNotFound, "USER_NOT_FOUND", "Nenhum admin ou professor com esse email"))
		return
	}
	if u.ID == userIDFrom(r) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Você já é o dono do quadro"))
		return
	}
	if err := s.upsertBoardMember(r.Context(), id, u.ID, in.Role); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": BoardMember{
		UserID: u.ID, Name: u.Name, Email: u.Email, Role: in.Role,
	}})
}

// DELETE /boards/{id}/members/{userId} — remove membro (só o dono).
func (s *Server) handleRemoveBoardMember(w http.ResponseWriter, r *http.Request) {
	id, err := boardIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	memberID, err := strconv.ParseInt(r.PathValue("userId"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "userId inválido"))
		return
	}
	if err := s.requireBoardOwner(w, r, id); err != nil {
		return
	}
	if err := s.deleteBoardMember(r.Context(), id, memberID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
