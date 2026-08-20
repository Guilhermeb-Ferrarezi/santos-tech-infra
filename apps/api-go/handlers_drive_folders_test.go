package main

// Testes de Arquivos (pastas do Google Drive) — só os caminhos que retornam
// ANTES de tocar no banco ou no Drive (padrão do repo: sem Postgres/Redis no
// CI, ver handlers_boards_test.go). s.drive fica nil em testServer(Config{}),
// então os handlers que dependem dele (list/download/upload) já respondem 503
// antes de qualquer query — cobertura de graceful-degradation "de graça".

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
)

func driveFolderReq(method, id, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/auth/admin/drive-folders/"+id, strings.NewReader(body))
	r.SetPathValue("id", id)
	return reqAs(r, userID)
}

func TestFolderAccessGuardNoToken(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.folderAccessGuard("read", func(http.ResponseWriter, *http.Request) {
		t.Fatal("não deveria passar do guard sem token")
	})(w, httptest.NewRequest("GET", "/drive-folders/"+validUUID+"/files", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestFolderAccessGuardBadUUID(t *testing.T) {
	// precisa passar pelo authGuard de verdade (cookie válido) pra alcançar o
	// checkuuid do folderAccessGuard — com s.db == nil, resolveToken não toca
	// no banco (mesmo truque de TestAuthGuard em server_test.go).
	cfg := Config{JWTSecret: "s-access", JWTRefreshSecret: "s-refresh"}
	s := testServer(cfg)
	access, _, _ := generateTokens(cfg.JWTSecret, cfg.JWTRefreshSecret, 1, "a@b.com", "")

	r := httptest.NewRequest("GET", "/drive-folders/não-é-uuid/files", nil)
	r.SetPathValue("id", "não-é-uuid")
	r.AddCookie(&http.Cookie{Name: "access_token", Value: access})
	w := httptest.NewRecorder()
	s.folderAccessGuard("read", func(http.ResponseWriter, *http.Request) {
		t.Fatal("não deveria passar do guard com UUID inválido")
	})(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestAccessRank(t *testing.T) {
	if accessRank("write") <= accessRank("read") {
		t.Error("write deveria valer mais que read")
	}
	if accessRank("read") <= accessRank("") {
		t.Error("read deveria valer mais que nenhum acesso")
	}
	if accessRank("") != 0 {
		t.Error("nível desconhecido/vazio deveria valer 0")
	}
}

func TestExtractDriveFolderID(t *testing.T) {
	cases := map[string]string{
		"1AbCdEfGhIjKlMnOpQr": "1AbCdEfGhIjKlMnOpQr",
		"https://drive.google.com/drive/folders/1AbC?usp=sharing": "1AbC",
		"https://drive.google.com/drive/folders/1AbC/":            "1AbC",
		"https://drive.google.com/drive/u/0/folders/1AbC":         "1AbC",
		"  1AbCdEfGh  ": "1AbCdEfGh",
	}
	for in, want := range cases {
		if got := extractDriveFolderID(in); got != want {
			t.Errorf("extractDriveFolderID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandleCreateDriveFolderValidation(t *testing.T) {
	s := testServer(Config{})

	// corpo inválido → 400
	w := httptest.NewRecorder()
	s.handleCreateDriveFolder(w, reqAs(httptest.NewRequest("POST", "/auth/admin/drive-folders", strings.NewReader("xxx")), 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d", w.Code)
	}

	// nome vazio → 400
	w2 := httptest.NewRecorder()
	s.handleCreateDriveFolder(w2, reqAs(httptest.NewRequest("POST", "/auth/admin/drive-folders", strings.NewReader(`{"name":"  ","driveFolderId":"abc"}`)), 1))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("nome vazio: code=%d", w2.Code)
	}

	// nome longo demais (> 120) → 400
	w3 := httptest.NewRecorder()
	long := `{"name":"` + strings.Repeat("a", 121) + `","driveFolderId":"abc"}`
	s.handleCreateDriveFolder(w3, reqAs(httptest.NewRequest("POST", "/auth/admin/drive-folders", strings.NewReader(long)), 1))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("nome longo: code=%d", w3.Code)
	}

	// driveFolderId vazio → 400
	w4 := httptest.NewRecorder()
	s.handleCreateDriveFolder(w4, reqAs(httptest.NewRequest("POST", "/auth/admin/drive-folders", strings.NewReader(`{"name":"Materiais"}`)), 1))
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("driveFolderId vazio: code=%d", w4.Code)
	}
}

func TestHandleDriveFolderBadUUID(t *testing.T) {
	s := testServer(Config{})
	for name, h := range map[string]http.HandlerFunc{
		"update": s.handleUpdateDriveFolder,
		"delete": s.handleDeleteDriveFolder,
		"access": s.handleGetDriveFolderAccess,
	} {
		w := httptest.NewRecorder()
		h(w, driveFolderReq("GET", "não-é-uuid", `{"name":"x","driveFolderId":"y"}`, 1))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: uuid inválido code=%d", name, w.Code)
		}
	}
}

func TestHandleSetDriveFolderAccessValidation(t *testing.T) {
	s := testServer(Config{})

	// roleKind inválido → 400
	w := httptest.NewRecorder()
	body := `{"roles":[{"roleKind":"errado","roleValue":"1","access":"read"}],"members":[]}`
	s.handleSetDriveFolderAccess(w, driveFolderReq("PUT", validUUID, body, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("roleKind inválido: code=%d", w.Code)
	}

	// cargo fixo inválido (role 3 = admin, não é atribuível) → 400
	w2 := httptest.NewRecorder()
	body2 := `{"roles":[{"roleKind":"fixed","roleValue":"3","access":"read"}],"members":[]}`
	s.handleSetDriveFolderAccess(w2, driveFolderReq("PUT", validUUID, body2, 1))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("cargo fixo inválido: code=%d", w2.Code)
	}

	// cargo personalizado com UUID inválido → 400
	w3 := httptest.NewRecorder()
	body3 := `{"roles":[{"roleKind":"custom","roleValue":"não-é-uuid","access":"write"}],"members":[]}`
	s.handleSetDriveFolderAccess(w3, driveFolderReq("PUT", validUUID, body3, 1))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("cargo personalizado inválido: code=%d", w3.Code)
	}

	// access inválido → 400
	w4 := httptest.NewRecorder()
	body4 := `{"roles":[{"roleKind":"fixed","roleValue":"1","access":"admin"}],"members":[]}`
	s.handleSetDriveFolderAccess(w4, driveFolderReq("PUT", validUUID, body4, 1))
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("access inválido: code=%d", w4.Code)
	}

	// cargo duplicado → 400
	w5 := httptest.NewRecorder()
	body5 := `{"roles":[{"roleKind":"fixed","roleValue":"1","access":"read"},{"roleKind":"fixed","roleValue":"1","access":"write"}],"members":[]}`
	s.handleSetDriveFolderAccess(w5, driveFolderReq("PUT", validUUID, body5, 1))
	if w5.Code != http.StatusBadRequest {
		t.Fatalf("cargo duplicado: code=%d", w5.Code)
	}

	// membro com access inválido → 400 (não chega a validar existência no banco)
	w6 := httptest.NewRecorder()
	body6 := `{"roles":[],"members":[{"userId":1,"access":"admin"}]}`
	s.handleSetDriveFolderAccess(w6, driveFolderReq("PUT", validUUID, body6, 1))
	if w6.Code != http.StatusBadRequest {
		t.Fatalf("membro access inválido: code=%d", w6.Code)
	}

	// membro duplicado → 400
	w7 := httptest.NewRecorder()
	body7 := `{"roles":[],"members":[{"userId":1,"access":"read"},{"userId":1,"access":"write"}]}`
	s.handleSetDriveFolderAccess(w7, driveFolderReq("PUT", validUUID, body7, 1))
	if w7.Code != http.StatusBadRequest {
		t.Fatalf("membro duplicado: code=%d", w7.Code)
	}
}

func TestHandleListDriveFolderFilesDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	w := httptest.NewRecorder()
	s.handleListDriveFolderFiles(w, driveFolderReq("GET", validUUID, "", 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleDownloadDriveFileDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	r := httptest.NewRequest("GET", "/drive-folders/"+validUUID+"/files/xyz/download", nil)
	r.SetPathValue("id", validUUID)
	r.SetPathValue("fileId", "xyz")
	w := httptest.NewRecorder()
	s.handleDownloadDriveFile(w, reqAs(r, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleUploadDriveFileDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	w := httptest.NewRecorder()
	s.handleUploadDriveFile(w, driveFolderReq("POST", validUUID, "", 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleRenameDriveFileDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	r := httptest.NewRequest("PATCH", "/drive-folders/"+validUUID+"/files/xyz", strings.NewReader(`{"name":"novo.png"}`))
	r.SetPathValue("id", validUUID)
	r.SetPathValue("fileId", "xyz")
	w := httptest.NewRecorder()
	s.handleRenameDriveFile(w, reqAs(r, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

// TestEnsureFileInFolderRejectsRootMutation cobre a falha de segurança corrigida:
// allowRoot=false (usado por rename/delete) tem que rejeitar fileID igual ao
// DriveFolderID da própria pasta ANTES de chamar o Drive — s.drive fica um
// DriveClient zero-value de propósito (nunca é usado, prova que o bloqueio é
// short-circuit e não depende de rede).
func TestIsInlineSafeContentType(t *testing.T) {
	safe := []string{"image/png", "image/jpeg", "video/mp4", "audio/mpeg", "application/pdf"}
	for _, ct := range safe {
		if !isInlineSafeContentType(ct) {
			t.Errorf("isInlineSafeContentType(%q) = false, esperava true", ct)
		}
	}
	unsafe := []string{
		"text/html", "image/svg+xml", "application/javascript",
		"application/octet-stream", "application/zip",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	}
	for _, ct := range unsafe {
		if isInlineSafeContentType(ct) {
			t.Errorf("isInlineSafeContentType(%q) = true, esperava false", ct)
		}
	}
}

func TestSanitizeFilenameForHeaderStripsBidiControl(t *testing.T) {
	// U+202E RIGHT-TO-LEFT OVERRIDE — usado pra disfarçar extensão de arquivo.
	name := "evil‮fdp.exe"
	got := sanitizeFilenameForHeader(name)
	if strings.ContainsRune(got, '‮') {
		t.Fatalf("sanitizeFilenameForHeader(%q) = %q, ainda contém RTLO", name, got)
	}
	if got != "evilfdp.exe" {
		t.Fatalf("sanitizeFilenameForHeader(%q) = %q, esperava %q", name, got, "evilfdp.exe")
	}
}

func TestEnsureFileInFolderRejectsRootMutation(t *testing.T) {
	s := testServer(Config{})
	s.drive = &DriveClient{}
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root-drive-id"}

	err := s.ensureFileInFolder(context.Background(), folder, "root-drive-id", false)
	if err == nil {
		t.Fatal("esperava erro ao tentar renomear/excluir a própria pasta raiz")
	}
	ae, ok := err.(*AppError)
	if !ok {
		t.Fatalf("esperava *AppError, veio %T", err)
	}
	if ae.Status != http.StatusForbidden {
		t.Fatalf("status=%d, esperava 403", ae.Status)
	}
}

func TestHandleDeleteDriveFileDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	r := httptest.NewRequest("DELETE", "/drive-folders/"+validUUID+"/files/xyz", nil)
	r.SetPathValue("id", validUUID)
	r.SetPathValue("fileId", "xyz")
	w := httptest.NewRecorder()
	s.handleDeleteDriveFile(w, reqAs(r, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleCreateDriveSubfolderDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	r := httptest.NewRequest("POST", "/drive-folders/"+validUUID+"/folders", strings.NewReader(`{"name":"Nova pasta"}`))
	r.SetPathValue("id", validUUID)
	w := httptest.NewRecorder()
	s.handleCreateDriveSubfolder(w, reqAs(r, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleMoveDriveFileDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	r := httptest.NewRequest("PATCH", "/drive-folders/"+validUUID+"/files/xyz/move", strings.NewReader(`{"toParent":"abc"}`))
	r.SetPathValue("id", validUUID)
	r.SetPathValue("fileId", "xyz")
	w := httptest.NewRecorder()
	s.handleMoveDriveFile(w, reqAs(r, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

// ── aninhamento de pastas cadastradas ───────────────────────────────────────
//
// A ACL é por pasta raiz e vale pra árvore toda: se "Escola/" é legível por
// aluno e "Escola/Financeiro/" só por admin, o aluno lê o Financeiro navegando
// pelo id da Escola. Cadastrar pastas aninhadas tem que ser recusado.

// driveTreeServer monta um Server com um DriveClient falso que responde
// `parents` a partir de uma árvore em memória (s.rdb == nil, então
// getOrSetJSON vai direto ao fetch).
func driveTreeServer(t *testing.T, parents map[string][]string) *Server {
	t.Helper()
	s := testServer(Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"parents": parents[id]})
	})
	return s
}

func TestCheckDriveFolderNestingRecusaDescendente(t *testing.T) {
	// financeiro está dentro de escola
	s := driveTreeServer(t, map[string][]string{"financeiro": {"escola"}})
	existing := []DriveFolder{{ID: "uuid-escola", Name: "Escola", DriveFolderID: "escola"}}

	err := s.checkDriveFolderNesting(context.Background(), "financeiro", "", existing)
	if err == nil {
		t.Fatal("cadastrar uma subpasta de pasta já cadastrada deveria ser recusado")
	}
	if !strings.Contains(err.Error(), "Escola") {
		t.Errorf("mensagem não cita a pasta conflitante: %v", err)
	}
}

func TestCheckDriveFolderNestingRecusaAncestral(t *testing.T) {
	// tentar cadastrar "escola" quando "financeiro" (dentro dela) já existe
	s := driveTreeServer(t, map[string][]string{"financeiro": {"escola"}})
	existing := []DriveFolder{{ID: "uuid-fin", Name: "Financeiro", DriveFolderID: "financeiro"}}

	if err := s.checkDriveFolderNesting(context.Background(), "escola", "", existing); err == nil {
		t.Fatal("cadastrar uma pasta que CONTÉM outra já cadastrada deveria ser recusado")
	}
}

func TestCheckDriveFolderNestingRecusaDuplicada(t *testing.T) {
	s := driveTreeServer(t, map[string][]string{})
	existing := []DriveFolder{{ID: "uuid-escola", Name: "Escola", DriveFolderID: "escola"}}

	if err := s.checkDriveFolderNesting(context.Background(), "escola", "", existing); err == nil {
		t.Fatal("cadastrar a mesma pasta do Drive duas vezes deveria ser recusado")
	}
}

func TestCheckDriveFolderNestingAceitaArvoresDisjuntas(t *testing.T) {
	s := driveTreeServer(t, map[string][]string{
		"escola":     {"raizA"},
		"financeiro": {"raizB"},
	})
	existing := []DriveFolder{{ID: "uuid-escola", Name: "Escola", DriveFolderID: "escola"}}

	if err := s.checkDriveFolderNesting(context.Background(), "financeiro", "", existing); err != nil {
		t.Fatalf("árvores disjuntas deveriam ser aceitas: %v", err)
	}
}

// TestCheckDriveFolderNestingIgnoraPropriaPastaNaEdicao: editar só o nome/
// descrição mantendo o mesmo driveFolderId não pode conflitar consigo mesma.
func TestCheckDriveFolderNestingIgnoraPropriaPastaNaEdicao(t *testing.T) {
	s := driveTreeServer(t, map[string][]string{})
	existing := []DriveFolder{{ID: "uuid-escola", Name: "Escola", DriveFolderID: "escola"}}

	if err := s.checkDriveFolderNesting(context.Background(), "escola", "uuid-escola", existing); err != nil {
		t.Fatalf("a própria pasta não pode conflitar consigo mesma: %v", err)
	}
}

// TestCheckDriveFolderNestingErroTransitorioNaoLibera: se o Drive está fora do
// ar, o cadastro é recusado (502) em vez de passar sem checagem.
func TestCheckDriveFolderNestingErroTransitorioNaoLibera(t *testing.T) {
	s := testServer(Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	existing := []DriveFolder{{ID: "uuid-escola", Name: "Escola", DriveFolderID: "escola"}}

	if err := s.checkDriveFolderNesting(context.Background(), "financeiro", "", existing); err == nil {
		t.Fatal("falha transitória do Drive não pode liberar o cadastro sem checagem")
	}
}
