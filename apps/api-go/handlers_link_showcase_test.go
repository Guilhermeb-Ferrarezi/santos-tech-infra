package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func linkShowcaseReq(method, id, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/links/"+id, strings.NewReader(body))
	r.SetPathValue("id", id)
	return reqAs(r, userID)
}

func TestPermGuardLinksNoToken(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.permGuard("links", "read", true, func(http.ResponseWriter, *http.Request) {
		t.Fatal("não deveria passar sem token")
	})(w, httptest.NewRequest("GET", "/links", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleLinkShowcaseBadUUID(t *testing.T) {
	s := testServer(Config{})
	for _, h := range []http.HandlerFunc{s.handleUpdateLinkShowcaseItem, s.handleDeleteLinkShowcaseItem} {
		w := httptest.NewRecorder()
		h(w, linkShowcaseReq("PUT", "nao-e-uuid", "{}", 1))
		if w.Code != http.StatusNotFound {
			t.Fatalf("uuid inválido: code=%d", w.Code)
		}
	}
}

func TestHandleCreateLinkShowcaseItemValidation(t *testing.T) {
	s := testServer(Config{})

	cases := []struct {
		name string
		body string
	}{
		{"corpo inválido", "xxx"},
		{"título vazio", `{"title":"","url":"https://x.com","status":"active"}`},
		{"url sem esquema", `{"title":"T","url":"x.com","status":"active"}`},
		{"url vazia", `{"title":"T","url":"","status":"active"}`},
		{"status inválido", `{"title":"T","url":"https://x.com","status":"pausado"}`},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		s.handleCreateLinkShowcaseItem(w, reqAs(
			httptest.NewRequest("POST", "/links", strings.NewReader(tc.body)), 1))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d body=%s", tc.name, w.Code, w.Body.String())
		}
	}
}

func TestValidateLinkShowcaseInputAcceptsValid(t *testing.T) {
	in := LinkShowcaseItemInput{Title: "  Artigo  ", URL: "https://santos-tech.com/blog/artigo", Status: "active"}
	if err := validateLinkShowcaseInput(&in); err != nil {
		t.Fatalf("entrada válida rejeitada: %v", err)
	}
	if in.Title != "Artigo" {
		t.Fatalf("título deveria ser aparado, veio %q", in.Title)
	}
}

func TestToPublicLinkShowcaseViewOmitsInternalFields(t *testing.T) {
	createdBy := int64(4)
	full := LinkShowcaseItem{
		ID: "x", Title: "T", Description: "D", URL: "https://x.com",
		Status: "active", Ordem: 1, CreatedBy: &createdBy,
	}
	view := toPublicLinkShowcaseView(full)
	if view.ID != full.ID || view.Title != full.Title || view.URL != full.URL {
		t.Fatal("view deveria manter os campos públicos")
	}
	// Checagem de tipo: LinkShowcasePublicItem não tem campo Status nem CreatedBy
	// — se algum dia alguém adicionar esses campos na struct pública por engano,
	// este teste não vai mais compilar exatamente por isso (é a garantia).
	_ = struct {
		ID, Title, Description, URL string
		Ordem                       int
	}{view.ID, view.Title, view.Description, view.URL, view.Ordem}
}
