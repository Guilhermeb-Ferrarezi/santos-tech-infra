package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPermGuardSocialNoToken(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.permGuard("social", "read", false, func(http.ResponseWriter, *http.Request) {
		t.Fatal("não deveria passar sem token")
	})(w, httptest.NewRequest("GET", "/social/posts", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", w.Code)
	}
}

func socialReq(method, id, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/social/posts/"+id, strings.NewReader(body))
	r.SetPathValue("id", id)
	return reqAs(r, userID)
}

func TestHandleSocialPostBadUUID(t *testing.T) {
	s := testServer(Config{})
	for _, h := range []http.HandlerFunc{
		s.handleGetSocialPost, s.handleUpdateSocialPost, s.handleDeleteSocialPost,
		s.handleUpdateSocialPostStatus, s.handleListSocialPostNotes, s.handleAddSocialPostNote,
	} {
		w := httptest.NewRecorder()
		h(w, socialReq("GET", "nao-e-uuid", "{}", 1))
		if w.Code != http.StatusNotFound {
			t.Fatalf("uuid inválido: code=%d", w.Code)
		}
	}
}

func TestHandleCreateSocialPostValidation(t *testing.T) {
	s := testServer(Config{})

	cases := []struct {
		name string
		body string
	}{
		{"corpo inválido", "xxx"},
		{"título vazio", `{"title":"","platform":"instagram","pilar":"educacional","status":"ideia"}`},
		{"plataforma inválida", `{"title":"T","platform":"snapchat","pilar":"educacional","status":"ideia"}`},
		{"pilar inválido", `{"title":"T","platform":"instagram","pilar":"fofoca","status":"ideia"}`},
		{"status inválido", `{"title":"T","platform":"instagram","pilar":"educacional","status":"deletado"}`},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		s.handleCreateSocialPost(w, reqAs(
			httptest.NewRequest("POST", "/social/posts", strings.NewReader(tc.body)), 1))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d", tc.name, w.Code)
		}
	}
}

func TestHandleUpdateSocialPostStatusValidation(t *testing.T) {
	s := testServer(Config{})

	w := httptest.NewRecorder()
	s.handleUpdateSocialPostStatus(w, socialReq("PATCH", validUUID, `{"status":"voando"}`, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status inválido: code=%d", w.Code)
	}
}

func TestHandleAddSocialPostNoteValidation(t *testing.T) {
	s := testServer(Config{})

	w := httptest.NewRecorder()
	s.handleAddSocialPostNote(w, socialReq("POST", validUUID, `{"content":"   "}`, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("conteúdo vazio: code=%d", w.Code)
	}
}
