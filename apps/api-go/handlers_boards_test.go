package main

// Testes dos quadros — só os caminhos de validação que retornam ANTES de tocar
// no banco (padrão do repo: sem Postgres/Redis no CI).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// reqAs injeta um userID no contexto, como o authGuard faria.
func reqAs(r *http.Request, userID int64) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userIDKey, userID))
}

func TestStaffGuardNoToken(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.staffGuard(func(http.ResponseWriter, *http.Request) {
		t.Fatal("não deveria passar do guard sem token")
	})(w, httptest.NewRequest("GET", "/boards", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleCreateBoardValidation(t *testing.T) {
	s := testServer(Config{})

	// corpo inválido → 400
	w := httptest.NewRecorder()
	s.handleCreateBoard(w, reqAs(httptest.NewRequest("POST", "/boards", strings.NewReader("xxx")), 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d", w.Code)
	}

	// título vazio → 400
	w2 := httptest.NewRecorder()
	s.handleCreateBoard(w2, reqAs(httptest.NewRequest("POST", "/boards", strings.NewReader(`{"title":"  "}`)), 1))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("título vazio: code=%d", w2.Code)
	}

	// título longo demais (> 200) → 400
	w3 := httptest.NewRecorder()
	long := `{"title":"` + strings.Repeat("a", 201) + `"}`
	s.handleCreateBoard(w3, reqAs(httptest.NewRequest("POST", "/boards", strings.NewReader(long)), 1))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("título longo: code=%d", w3.Code)
	}
}

// boardReq monta uma request com o PathValue {id} preenchido (como o mux faria).
func boardReq(method, id, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/boards/"+id, strings.NewReader(body))
	r.SetPathValue("id", id)
	return reqAs(r, userID)
}

const validUUID = "3f9c2a10-1234-4abc-8def-0123456789ab"

func TestHandleBoardBadUUID(t *testing.T) {
	s := testServer(Config{})
	// UUID malformado → 404 (indistinguível de inexistente), antes do banco
	for _, h := range []http.HandlerFunc{s.handleGetBoard, s.handleUpdateBoard, s.handleDeleteBoard, s.handleListBoardMembers, s.handleAddBoardMember} {
		w := httptest.NewRecorder()
		h(w, boardReq("GET", "não-é-uuid", "{}", 1))
		if w.Code != http.StatusNotFound {
			t.Fatalf("uuid inválido: code=%d", w.Code)
		}
	}
}

func TestHandleUpdateBoardValidation(t *testing.T) {
	s := testServer(Config{})

	// corpo inválido → 400
	w := httptest.NewRecorder()
	s.handleUpdateBoard(w, boardReq("PUT", validUUID, "xxx", 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d", w.Code)
	}

	// sem scene/sceneVersion → 400
	w2 := httptest.NewRecorder()
	s.handleUpdateBoard(w2, boardReq("PUT", validUUID, `{"title":"x"}`, 1))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("sem scene: code=%d", w2.Code)
	}

	// cena acima de 10MB → 413
	w3 := httptest.NewRecorder()
	big := `{"scene":{"x":"` + strings.Repeat("a", maxBoardScene+8192) + `"},"sceneVersion":1}`
	s.handleUpdateBoard(w3, boardReq("PUT", validUUID, big, 1))
	if w3.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("cena grande: code=%d", w3.Code)
	}
}

func TestHandleAddBoardMemberValidation(t *testing.T) {
	s := testServer(Config{})

	// email inválido → 400
	w := httptest.NewRecorder()
	s.handleAddBoardMember(w, boardReq("POST", validUUID, `{"email":"sem-arroba","role":"editor"}`, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("email inválido: code=%d", w.Code)
	}

	// papel inválido → 400
	w2 := httptest.NewRecorder()
	s.handleAddBoardMember(w2, boardReq("POST", validUUID, `{"email":"a@b.com","role":"dono"}`, 1))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("papel inválido: code=%d", w2.Code)
	}
}

func TestHandleRemoveBoardMemberBadUserID(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("DELETE", "/boards/"+validUUID+"/members/abc", nil)
	r.SetPathValue("id", validUUID)
	r.SetPathValue("userId", "abc")
	w := httptest.NewRecorder()
	s.handleRemoveBoardMember(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("userId inválido: code=%d", w.Code)
	}
}
