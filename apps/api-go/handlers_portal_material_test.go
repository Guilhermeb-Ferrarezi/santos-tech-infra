package main

// Testes dos handlers do Material vivo — só os caminhos que retornam ANTES de
// tocar o banco (padrão do repo, ver handlers_portal_diario_test.go) e o
// guard via usuário em cache no miniredis.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func materialReq(method, courseID, version, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/portal/courses/"+courseID+"/doc", strings.NewReader(body))
	r.SetPathValue("courseId", courseID)
	if version != "" {
		r.SetPathValue("version", version)
	}
	return reqAs(r, userID)
}

func TestPortalMaterialBadIDsBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for name, h := range map[string]http.HandlerFunc{
		"get": s.handlePortalGetCourseDoc, "put": s.handlePortalPutCourseDoc, "seed": s.handlePortalSeedCourseDoc,
		"revisions": s.handlePortalCourseDocRevisions, "revision": s.handlePortalCourseDocRevision,
		"restore": s.handlePortalRestoreCourseDoc, "me": s.handlePortalMyCourseDoc,
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h(w, materialReq("GET", "x", "1", `{"bodyMd":"## A\n\ntexto"}`, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("courseId inválido: code=%d want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
	for name, h := range map[string]http.HandlerFunc{"revision": s.handlePortalCourseDocRevision, "restore": s.handlePortalRestoreCourseDoc} {
		t.Run(name+" versão inválida", func(t *testing.T) {
			w := httptest.NewRecorder()
			h(w, materialReq("GET", "1", "0", "", 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("version inválida: code=%d want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestPortalPutCourseDocValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for name, body := range map[string]string{
		"corpo inválido":      "xxx",
		"campo desconhecido":  `{"bodyMd":"## A","xyz":true}`,
		"bodyMd vazio":        `{"bodyMd":"   "}`,
		"bodyMd longo demais": `{"bodyMd":"` + strings.Repeat("a", materialBodyMax+1) + `"}`,
		"corpo acima do teto": `{"bodyMd":"` + strings.Repeat("a", materialBodyBytesMax+1) + `"}`,
		"version negativa":    `{"bodyMd":"## A","version":-1}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.handlePortalPutCourseDoc(w, materialReq("PUT", "1", "", body, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
	// version é um campo conhecido (concorrência otimista — ver
	// courseDocSalvarManual): body com version válida não pode ser rejeitado
	// por "campo desconhecido" (DisallowUnknownFields).
	var inComVersao materialPutInput
	if err := decodePortalJSON(strings.NewReader(`{"bodyMd":"## A\n\ntexto","version":3}`), &inComVersao); err != nil {
		t.Fatalf("version deveria ser aceita: %v", err)
	}
	if err := inComVersao.validate(); err != nil || inComVersao.Version != 3 {
		t.Errorf("version=%d err=%v, want 3 sem erro", inComVersao.Version, err)
	}
	// validate normaliza: CRLF → LF, aparas, quebra de linha no fim.
	in := materialPutInput{BodyMd: "  ## A\r\n\r\ntexto  "}
	if err := in.validate(); err != nil || in.BodyMd != "## A\n\ntexto\n" {
		t.Errorf("normalização: %q err=%v", in.BodyMd, err)
	}
	// Cerca de código aberta (edição manual incompleta) é fechada, não
	// rejeitada — ver materialFecharCercaAberta.
	aberta := materialPutInput{BodyMd: "## A\n\n```python\nprint(1)"}
	if err := aberta.validate(); err != nil {
		t.Fatalf("cerca aberta não deveria ser rejeitada: %v", err)
	}
	if strings.Count(aberta.BodyMd, "```")%2 != 0 {
		t.Errorf("cerca deveria ter sido fechada: %q", aberta.BodyMd)
	}
}

func TestPortalMaterialRoutesRequireAuthBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for _, tc := range []struct {
		name   string
		h      http.HandlerFunc
		method string
		path   string
	}{
		{"ler", s.portalRead("portal_cursos", s.handlePortalGetCourseDoc), "GET", "/portal/courses/1/doc"},
		{"editar", s.portalWrite("portal_cursos", s.handlePortalPutCourseDoc), "PUT", "/portal/courses/1/doc"},
		{"semear", s.portalWrite("portal_cursos", s.handlePortalSeedCourseDoc), "POST", "/portal/courses/1/doc/seed"},
		{"histórico", s.portalRead("portal_cursos", s.handlePortalCourseDocRevisions), "GET", "/portal/courses/1/doc/revisions"},
		{"restaurar", s.portalWrite("portal_cursos", s.handlePortalRestoreCourseDoc), "POST", "/portal/courses/1/doc/revisions/1/restore"},
		{"aluno", s.authGuard(s.handlePortalMyCourseDoc), "GET", "/portal/me/courses/1/doc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.h(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

// Guard das rotas do staff: professor lê mas NÃO escreve (portal_cursos é
// área de admin), aluno não entra, cargo só com portal_cursos:read|write.
func TestPortalMaterialGuard(t *testing.T) {
	cfg := Config{JWTSecret: "s-access", JWTRefreshSecret: "s-refresh"}
	s := testServerWithRedis(t, cfg)
	cargo := "44444444-4444-4444-4444-444444444444"
	b, _ := json.Marshal(map[string][]string{"portal_cursos": {"read"}})
	if err := s.rdb.Set(context.Background(), "api-go:auth:custom_role:"+cargo, b, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	professor := seedCachedUser(t, s, User{ID: 41, Email: "prof@santos-tech.com", Name: "Prof", Role: RoleTeacher})
	aluno := seedCachedUser(t, s, User{ID: 42, Email: "aluno@x.com", Name: "Aluno", Role: RoleStudent})
	admin := seedCachedUser(t, s, User{ID: 43, Email: "adm@santos-tech.com", Name: "Adm", Role: RoleAdmin})
	soLeitura := seedCachedUser(t, s, User{ID: 44, Email: "c@santos-tech.com", Role: RoleCustom, CustomRoleID: &cargo})

	stub := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	for _, tc := range []struct {
		name   string
		guard  http.HandlerFunc
		cookie *http.Cookie
		want   int
	}{
		{"professor lê", s.portalRead("portal_cursos", stub), professor, http.StatusNoContent},
		{"professor não semeia", s.portalWrite("portal_cursos", stub), professor, http.StatusForbidden},
		{"admin semeia", s.portalWrite("portal_cursos", stub), admin, http.StatusNoContent},
		{"aluno não lê o do staff", s.portalRead("portal_cursos", stub), aluno, http.StatusForbidden},
		{"aluno passa no authGuard do /me", s.authGuard(stub), aluno, http.StatusNoContent},
		{"cargo só leitura lê", s.portalRead("portal_cursos", stub), soLeitura, http.StatusNoContent},
		{"cargo só leitura não edita", s.portalWrite("portal_cursos", stub), soLeitura, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/portal/courses/1/doc", nil)
			r.AddCookie(tc.cookie)
			w := httptest.NewRecorder()
			tc.guard(w, r)
			if w.Code != tc.want {
				t.Fatalf("code=%d want %d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// Sem asynq (s.queue == nil) o enqueue avisa em vez de tocar o banco; o
// gancho pós-aula é silencioso (só loga) nessa situação.
func TestEnqueueMaterialSemFila(t *testing.T) {
	s := testServer(Config{})
	if err := s.enqueueMaterial(context.Background(), materialPayload{CourseID: 1, Versao: time.Now()}); err != errFilaIndisponivel {
		t.Errorf("err=%v want errFilaIndisponivel", err)
	}
	s.materialEnfileirarAposAula(context.Background(), 1, 1) // não pode entrar em panic sem fila nem banco
}
