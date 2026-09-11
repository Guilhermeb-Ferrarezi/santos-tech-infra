package main

// Testes do Diário de aula — só os caminhos que retornam ANTES de tocar o
// banco (padrão do repo: sem Postgres no CI, ver handlers_portal_test.go).
// O guard, que normalmente precisa do banco pra carregar o usuário, é
// exercitado via cache: cachedUserByID lê o Redis (miniredis) antes de cair
// no banco, então um usuário pré-gravado na chave de cache basta pra decidir
// professor/aluno/cargo sem s.db. Upsert, fila e o 404 de matrícula do
// download continuam sem cobertura automatizada (dependem de banco real).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func diaryReq(method, sessionID, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/portal/sessions/"+sessionID+"/diary", strings.NewReader(body))
	r.SetPathValue("sessionId", sessionID)
	return reqAs(r, userID)
}

func TestPortalDiaryBadIDBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for name, h := range map[string]http.HandlerFunc{"get": s.handlePortalGetDiary, "put": s.handlePortalPutDiary} {
		w := httptest.NewRecorder()
		h(w, diaryReq("PUT", "x", `{"summary":"a","attachments":[]}`, 1))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: sessionId inválido code=%d want %d", name, w.Code, http.StatusBadRequest)
		}
	}
}

func TestPortalPutDiaryValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})
	anexo := func(id, name string) string {
		return `{"driveFileId":"` + id + `","name":"` + name + `","mimeType":"application/pdf","size":10}`
	}
	muitos := make([]string, portalDiaryAttachmentsMax+1)
	for i := range muitos {
		muitos[i] = anexo("f", "a.pdf")
	}
	cases := []struct {
		name string
		body string
	}{
		{"corpo inválido", "xxx"},
		{"campo desconhecido", `{"summary":"a","attachments":[],"autor":"x"}`},
		{"resumo longo demais", `{"summary":"` + strings.Repeat("a", portalDiarySummaryMax+1) + `","attachments":[]}`},
		{"anexos demais", `{"summary":"a","attachments":[` + strings.Join(muitos, ",") + `]}`},
		{"anexo sem driveFileId", `{"summary":"a","attachments":[` + anexo("  ", "a.pdf") + `]}`},
		{"anexo sem nome", `{"summary":"a","attachments":[` + anexo("f", "") + `]}`},
		{"anexo com tamanho negativo", `{"summary":"a","attachments":[{"driveFileId":"f","name":"a","mimeType":"","size":-1}]}`},
		{"videoUrl ftp", `{"summary":"a","attachments":[],"videoUrl":"ftp://x/y.mp4"}`},
		{"videoUrl javascript", `{"summary":"a","attachments":[],"videoUrl":"javascript:alert(1)"}`},
		{"videoUrl sem host", `{"summary":"a","attachments":[],"videoUrl":"https://"}`},
		{"videoUrl longa demais", `{"summary":"a","attachments":[],"videoUrl":"https://x/` + strings.Repeat("a", portalDiaryVideoURLMax) + `"}`},
		{"videoDriveFileId longo demais", `{"summary":"a","attachments":[],"videoDriveFileId":"` + strings.Repeat("a", portalDiaryTextMax+1) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.handlePortalPutDiary(w, diaryReq("PUT", "1", tc.body, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
}

// O PUT tem teto próprio de corpo (256 KiB): maior que isso é 400, não 500.
func TestPortalPutDiaryBodyTooLargeBeforeDB(t *testing.T) {
	s := testServer(Config{})
	body := `{"summary":"` + strings.Repeat("a", portalDiaryBodyMax+1) + `","attachments":[]}`
	w := httptest.NewRecorder()
	s.handlePortalPutDiary(w, diaryReq("PUT", "1", body, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo acima do teto: code=%d want %d", w.Code, http.StatusBadRequest)
	}
}

func TestPortalDiaryInputValidateNormalizes(t *testing.T) {
	var in portalDiaryInput
	if err := decodePortalJSON(strings.NewReader(`{"summary":"Aula de Python","videoUrl":"  ","videoDriveFileId":"","attachments":[{"driveFileId":" abc ","name":" foo.pdf ","mimeType":" application/pdf ","size":3}]}`), &in); err != nil {
		t.Fatal(err)
	}
	if err := in.validate(); err != nil {
		t.Fatalf("input válido rejeitado: %v", err)
	}
	if in.VideoURL != nil || in.VideoDriveFileID != nil {
		t.Errorf("vídeo vazio deveria virar nil: url=%v drive=%v", in.VideoURL, in.VideoDriveFileID)
	}
	if got := in.Attachments[0]; got.DriveFileID != "abc" || got.Name != "foo.pdf" || got.MimeType != "application/pdf" {
		t.Errorf("anexo não aparado: %+v", got)
	}

	// attachments ausente → lista vazia (o JSONB é '[]', nunca NULL)
	var semAnexos portalDiaryInput
	if err := decodePortalJSON(strings.NewReader(`{"summary":"x"}`), &semAnexos); err != nil {
		t.Fatal(err)
	}
	if err := semAnexos.validate(); err != nil {
		t.Fatal(err)
	}
	if semAnexos.Attachments == nil || len(semAnexos.Attachments) != 0 {
		t.Errorf("attachments deveria ser lista vazia, veio %#v", semAnexos.Attachments)
	}

	// videoUrl https válida é preservada (aparada)
	url := " https://youtu.be/abc "
	comVideo := portalDiaryInput{VideoURL: &url}
	if err := comVideo.validate(); err != nil {
		t.Fatal(err)
	}
	if comVideo.VideoURL == nil || *comVideo.VideoURL != "https://youtu.be/abc" {
		t.Errorf("videoUrl=%v", comVideo.VideoURL)
	}
}

func TestPortalDiaryDaysFrom(t *testing.T) {
	for raw, want := range map[string]int{"": 30, "7": 7, "0": 1, "-3": 1, "400": 365, " 15 ": 15} {
		got, err := portalDiaryDaysFrom(raw)
		if err != nil || got != want {
			t.Errorf("days=%q → %d (err=%v), want %d", raw, got, err, want)
		}
	}
	if _, err := portalDiaryDaysFrom("ontem"); err == nil {
		t.Error("days não numérico deveria ser 400")
	}
}

func TestPortalDiaryPendingBadDaysBeforeDB(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handlePortalDiaryPending(w, reqAs(httptest.NewRequest("GET", "/portal/diary/pending?days=abc", nil), 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("days inválido: code=%d want %d", w.Code, http.StatusBadRequest)
	}
}

func TestPortalDiaryParseAttachments(t *testing.T) {
	if got := portalDiaryParseAttachments(""); got == nil || len(got) != 0 {
		t.Errorf("vazio → lista vazia, veio %#v", got)
	}
	if got := portalDiaryParseAttachments("null"); got == nil || len(got) != 0 {
		t.Errorf("null → lista vazia, veio %#v", got)
	}
	if got := portalDiaryParseAttachments("{corrompido"); got == nil || len(got) != 0 {
		t.Errorf("json inválido → lista vazia, veio %#v", got)
	}
	got := portalDiaryParseAttachments(`[{"driveFileId":"f1","name":"a.pdf","mimeType":"application/pdf","size":12}]`)
	if len(got) != 1 || got[0].DriveFileID != "f1" || got[0].Size != 12 {
		t.Errorf("anexo não lido: %#v", got)
	}
}

// seedCachedUser grava o usuário na chave de cache que cachedUserByID lê —
// com s.db == nil é o único jeito de o guard "carregar" alguém. Devolve o
// cookie de sessão desse usuário.
func seedCachedUser(t *testing.T, s *Server, u User) *http.Cookie {
	t.Helper()
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.rdb.Set(context.Background(), cacheUserKey(u.ID), b, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	access, _, err := generateTokens(s.cfg.JWTSecret, s.cfg.JWTRefreshSecret, u.ID, u.Email, u.Name)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "access_token", Value: access}
}

// Guard do diário: professor passa (leitura E escrita — a exceção do portal),
// aluno não, cargo personalizado só com portal_diario:<ação>.
func TestPortalDiarioGuard(t *testing.T) {
	cfg := Config{JWTSecret: "s-access", JWTRefreshSecret: "s-refresh"}
	s := testServerWithRedis(t, cfg)

	cargoComDiario := "11111111-1111-1111-1111-111111111111"
	cargoSemDiario := "22222222-2222-2222-2222-222222222222"
	for id, perms := range map[string]map[string][]string{
		cargoComDiario: {"portal_diario": {"read", "write"}},
		cargoSemDiario: {"portal_turmas": {"read", "write"}},
	} {
		b, _ := json.Marshal(perms)
		if err := s.rdb.Set(context.Background(), "api-go:auth:custom_role:"+id, b, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
	}

	professor := seedCachedUser(t, s, User{ID: 21, Email: "prof@santos-tech.com", Name: "Prof", Role: RoleTeacher})
	aluno := seedCachedUser(t, s, User{ID: 22, Email: "aluno@x.com", Name: "Aluno", Role: RoleStudent})
	admin := seedCachedUser(t, s, User{ID: 23, Email: "adm@santos-tech.com", Name: "Adm", Role: RoleAdmin})
	comPerm := seedCachedUser(t, s, User{ID: 24, Email: "c1@santos-tech.com", Role: RoleCustom, CustomRoleID: &cargoComDiario})
	semPerm := seedCachedUser(t, s, User{ID: 25, Email: "c2@santos-tech.com", Role: RoleCustom, CustomRoleID: &cargoSemDiario})

	for _, tc := range []struct {
		name   string
		action string
		cookie *http.Cookie
		want   int
	}{
		{"professor lê", "read", professor, http.StatusNoContent},
		{"professor escreve", "write", professor, http.StatusNoContent},
		{"admin escreve", "write", admin, http.StatusNoContent},
		{"cargo com portal_diario escreve", "write", comPerm, http.StatusNoContent},
		{"aluno lê", "read", aluno, http.StatusForbidden},
		{"aluno escreve", "write", aluno, http.StatusForbidden},
		{"cargo sem portal_diario lê", "read", semPerm, http.StatusForbidden},
		{"sem sessão", "read", nil, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/portal/sessions/1/diary", nil)
			if tc.cookie != nil {
				r.AddCookie(tc.cookie)
			}
			w := httptest.NewRecorder()
			s.portalDiario(tc.action, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})(w, r)
			if w.Code != tc.want {
				t.Fatalf("code=%d want %d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestPortalMyDiaryFileRequiresAuthBeforeDB(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("GET", "/portal/me/diary/files/abc", nil)
	r.SetPathValue("fileId", "abc")
	w := httptest.NewRecorder()
	s.authGuard(s.handlePortalMyDiaryFile)(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want %d", w.Code, http.StatusUnauthorized)
	}
}

// Sem Drive configurado (s.drive == nil) o download do aluno degrada com 503
// antes de qualquer consulta — mesmo comportamento do /drive-folders.
func TestPortalMyDiaryFileDriveDisabledBeforeDB(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("GET", "/portal/me/diary/files/abc", nil)
	r.SetPathValue("fileId", "abc")
	w := httptest.NewRecorder()
	s.handlePortalMyDiaryFile(w, reqAs(r, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestPortalSetStudentIndividualContractedContentValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})
	body := `{"individual":true,"contractedContent":"` + strings.Repeat("a", portalContractedContentMax+1) + `"}`
	r := httptest.NewRequest("PATCH", "/portal/classes/1/students/1", strings.NewReader(body))
	r.SetPathValue("classId", "1")
	r.SetPathValue("studentId", "1")
	w := httptest.NewRecorder()
	s.handlePortalSetStudentIndividual(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("contractedContent longo demais: code=%d want %d", w.Code, http.StatusBadRequest)
	}
}

// Anexo ou vídeo só entram se apontarem pra dentro da pasta "Diário de aulas"
// — e essa checagem precisa do Drive. Sem Drive configurado, o PUT com
// arquivo é recusado ANTES de tocar em usuário ou banco; sem arquivo nenhum,
// a checagem nem entra em cena (o resumo continua salvável).
func TestPortalPutDiaryFilesNeedDrive(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handlePortalPutDiary(w, diaryReq("PUT", "1", `{"summary":"a","attachments":[{"driveFileId":"abc","name":"a.py","mimeType":"text/plain","size":1}]}`, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT com anexo sem Drive: code=%d want %d body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handlePortalPutDiary(w, diaryReq("PUT", "1", `{"summary":"a","attachments":[],"videoDriveFileId":"vid"}`, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT com vídeo sem Drive: code=%d want %d", w.Code, http.StatusServiceUnavailable)
	}
}
