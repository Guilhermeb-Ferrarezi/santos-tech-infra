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
		s.handleConfirmSocialPostPlatform, s.handleUnconfirmSocialPostPlatform,
	} {
		w := httptest.NewRecorder()
		h(w, socialReq("GET", "nao-e-uuid", "{}", 1))
		if w.Code != http.StatusNotFound {
			t.Fatalf("uuid inválido: code=%d", w.Code)
		}
	}
}

func socialPlatformReq(method, id, platform string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/social/posts/"+id+"/publish-confirmations/"+platform, nil)
	r.SetPathValue("id", id)
	r.SetPathValue("platform", platform)
	return reqAs(r, userID)
}

func TestHandleConfirmSocialPostPlatformValidation(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleConfirmSocialPostPlatform(w, socialPlatformReq("POST", validUUID, "myspace", 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("plataforma inválida: code=%d", w.Code)
	}
}

func TestCheckPublishConfirmationsCompleteNoTargets(t *testing.T) {
	s := testServer(Config{})
	// Sem plataformasDestino não há o que checar — não deve tentar ir ao banco
	// (que é nil em testServer; se tentasse, este teste travaria/panicaria).
	if err := s.checkPublishConfirmationsComplete(t.Context(), validUUID, nil); err != nil {
		t.Fatalf("esperava nil (nada pra checar), veio: %v", err)
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

// --- Dono por plataforma (2026-08-17) ---
//
// NOTA sobre cobertura: testServer(Config{}) roda com s.db == nil (harness sem
// Postgres real — não há testcontainers/docker-compose/TEST_DATABASE_URL para
// Go neste repo; o .env.test na raiz não é consumido por nenhum teste Go, só
// por outra stack). Isso já é a convenção do resto do pacote (ver
// TestAdminGuardNoToken, TestListCustomRolesNoToken etc.): guards só são
// testados no caminho "sem token" (401, antes de tocar o banco) e handlers só
// são testados nos ramos de validação que retornam antes de qualquer chamada a
// s.db. Os seguintes casos do plano NÃO têm cobertura automatizada possível
// com este harness, por dependerem de banco real:
//   - Confirmar/desconfirmar com dono configurado ≠ usuário da requisição → 403
//   - Confirmar/desconfirmar com usuário == dono → sucesso
//   - Confirmar/desconfirmar sem dono configurado → sucesso (fail-open)
//   - PUT/DELETE /social/platform-owners/{platform} por role não-admin → 403
//     (adminGuard só decide isso depois de cachedUserByID, que toca o banco)
//   - GET /social/platform-owners com social:read → 200 com dados reais
//   - getSocialPlatformOwner retornando nil sem erro quando não há dono (a
//     própria função faz s.db.QueryRow; sem um banco real não dá pra exercitar
//     nem o caminho "sem linha" nem o caminho "com linha")
// Ficam registrados como pendência de teste de integração (ver PENDENCIAS.md).

func TestListSocialPlatformOwnersNoToken(t *testing.T) {
	s := testServer(Config{})
	h := s.permGuard("social", "read", false, s.handleListSocialPlatformOwners)
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/social/platform-owners", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, esperado 401", w.Code)
	}
}

func TestSetSocialPlatformOwnerNoToken(t *testing.T) {
	s := testServer(Config{})
	h := s.adminGuard(s.handleSetSocialPlatformOwner)
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("PUT", "/social/platform-owners/instagram", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, esperado 401", w.Code)
	}
}

func TestDeleteSocialPlatformOwnerNoToken(t *testing.T) {
	s := testServer(Config{})
	h := s.adminGuard(s.handleDeleteSocialPlatformOwner)
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("DELETE", "/social/platform-owners/instagram", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, esperado 401", w.Code)
	}
}

// Plataforma inválida é rejeitada antes de qualquer acesso ao banco — testável
// chamando o handler diretamente (sem o guard).
func TestHandleSetSocialPlatformOwnerBadPlatform(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("PUT", "/social/platform-owners/myspace", strings.NewReader(`{"userId":1}`))
	r.SetPathValue("platform", "myspace")
	w := httptest.NewRecorder()
	s.handleSetSocialPlatformOwner(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("plataforma inválida: code=%d", w.Code)
	}
}

// Corpo inválido (JSON quebrado ou userId <= 0) é rejeitado antes de chamar
// s.cachedUserByID — não toca o banco.
func TestHandleSetSocialPlatformOwnerBadBody(t *testing.T) {
	s := testServer(Config{})
	for _, body := range []string{"xxx", `{"userId":0}`, `{"userId":-1}`, `{}`} {
		r := httptest.NewRequest("PUT", "/social/platform-owners/instagram", strings.NewReader(body))
		r.SetPathValue("platform", "instagram")
		w := httptest.NewRecorder()
		s.handleSetSocialPlatformOwner(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%q code=%d", body, w.Code)
		}
	}
}

func TestHandleDeleteSocialPlatformOwnerBadPlatform(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("DELETE", "/social/platform-owners/myspace", nil)
	r.SetPathValue("platform", "myspace")
	w := httptest.NewRecorder()
	s.handleDeleteSocialPlatformOwner(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("plataforma inválida: code=%d", w.Code)
	}
}

// handleUnconfirmSocialPostPlatform ganhou (nesta feature) a mesma validação
// de plataforma que handleConfirmSocialPostPlatform já tinha — bug pré-existente
// pequeno, corrigido de passagem (ver plano Task 3 Step 2). Plataforma inválida
// retorna 400 antes de checar dono ou post, então não toca o banco.
func TestHandleUnconfirmSocialPostPlatformValidation(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleUnconfirmSocialPostPlatform(w, socialPlatformReq("DELETE", validUUID, "myspace", 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("plataforma inválida: code=%d", w.Code)
	}
}
