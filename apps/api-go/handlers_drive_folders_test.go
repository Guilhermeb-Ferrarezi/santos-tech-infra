package main

// Testes de Arquivos (pastas do Google Drive) — só os caminhos que retornam
// ANTES de tocar no banco ou no Drive (padrão do repo: sem Postgres/Redis no
// CI, ver handlers_boards_test.go). s.drive fica nil em testServer(Config{}),
// então os handlers que dependem dele (list/download/upload) já respondem 503
// antes de qualquer query — cobertura de graceful-degradation "de graça".

import (
	"net/http"
	"net/http/httptest"
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
