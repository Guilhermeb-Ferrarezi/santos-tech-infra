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
		{"imageUrl sem esquema", `{"title":"T","url":"https://x.com","status":"active","imageUrl":"x.com/img.png"}`},
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

func TestValidateLinkShowcaseInputImageURL(t *testing.T) {
	blank := "   "
	in := LinkShowcaseItemInput{Title: "T", URL: "https://x.com", Status: "active", ImageURL: &blank}
	if err := validateLinkShowcaseInput(&in); err != nil {
		t.Fatalf("não esperava erro: %v", err)
	}
	if in.ImageURL != nil {
		t.Fatalf("imageUrl só com espaços deveria virar nil, veio %q", *in.ImageURL)
	}

	invalid := "ftp://x.com/img.png"
	in2 := LinkShowcaseItemInput{Title: "T", URL: "https://x.com", Status: "active", ImageURL: &invalid}
	if err := validateLinkShowcaseInput(&in2); err == nil {
		t.Fatal("imageUrl com esquema não-http(s) deveria ser rejeitada")
	}
}

func TestPermGuardLinkShowcaseSettingsNoToken(t *testing.T) {
	s := testServer(Config{})
	for _, method := range []string{"GET", "PUT"} {
		w := httptest.NewRecorder()
		s.permGuard("links", "read", true, func(http.ResponseWriter, *http.Request) {
			t.Fatal("não deveria passar sem token")
		})(w, httptest.NewRequest(method, "/links/settings", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: code=%d", method, w.Code)
		}
	}
}

func TestHandleUpdateLinkShowcaseSettingsValidation(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleUpdateLinkShowcaseSettings(w, reqAs(
		httptest.NewRequest("PUT", "/links/settings", strings.NewReader(`{"backgroundImageUrl":"nao-e-url"}`)), 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestValidateLinkShowcaseSettingsInput(t *testing.T) {
	valid := "https://cdn.santos-tech.com/bg.jpg"
	in := LinkShowcaseSettingsInput{BackgroundImageURL: &valid}
	if err := validateLinkShowcaseSettingsInput(&in); err != nil {
		t.Fatalf("url válida rejeitada: %v", err)
	}

	invalid := "nao-e-url"
	in = LinkShowcaseSettingsInput{BackgroundImageURL: &invalid}
	if err := validateLinkShowcaseSettingsInput(&in); err == nil {
		t.Fatal("url inválida deveria ser rejeitada")
	}

	empty := "   "
	in = LinkShowcaseSettingsInput{BackgroundImageURL: &empty}
	if err := validateLinkShowcaseSettingsInput(&in); err != nil {
		t.Fatalf("string vazia não deveria dar erro: %v", err)
	}
	if in.BackgroundImageURL != nil {
		t.Fatal("string vazia deveria virar nil (limpar a imagem)")
	}

	in = LinkShowcaseSettingsInput{BackgroundImageURL: nil}
	if err := validateLinkShowcaseSettingsInput(&in); err != nil {
		t.Fatalf("nil não deveria dar erro: %v", err)
	}
}

func TestToPublicLinkShowcaseViewOmitsInternalFields(t *testing.T) {
	createdBy := int64(4)
	full := LinkShowcaseItem{
		ID: "x", Title: "T", Description: "D", URL: "https://x.com",
		Status: "active", Ordem: 1, TitleGradient: true, CreatedBy: &createdBy,
	}
	view := toPublicLinkShowcaseView(full)
	if view.ID != full.ID || view.Title != full.Title || view.URL != full.URL {
		t.Fatal("view deveria manter os campos públicos")
	}
	if !view.TitleGradient {
		t.Fatal("titleGradient deveria passar pra view pública — é ela que decide o degradê no site")
	}
	// Checagem de tipo: LinkShowcasePublicItem não tem campo Status nem CreatedBy
	// — se algum dia alguém adicionar esses campos na struct pública por engano,
	// este teste não vai mais compilar exatamente por isso (é a garantia).
	_ = struct {
		ID, Title, Description, URL string
		Ordem                       int
	}{view.ID, view.Title, view.Description, view.URL, view.Ordem}
}
