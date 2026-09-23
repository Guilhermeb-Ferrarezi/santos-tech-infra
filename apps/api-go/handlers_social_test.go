package main

import (
	"fmt"
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

// socialPlatformReqBody é socialPlatformReq com corpo — usado pela validação de
// link obrigatório (2026-09-23): POST .../publish-confirmations/{platform}
// passou a exigir {"url": string} no corpo.
func socialPlatformReqBody(method, id, platform, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/social/posts/"+id+"/publish-confirmations/"+platform, strings.NewReader(body))
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

// Confirmar sem o link real da publicação (prova de que saiu no ar, exigida
// desde 23/09/2026) é rejeitado antes de checar dono ou post — não toca o banco
// (nil em testServer(Config{})), mesma convenção do resto do arquivo. Corpo
// ausente/vazio/só espaço → "Cole o link da publicação antes de confirmar.";
// corpo malformado → "Corpo inválido" (erro de decode, mensagem diferente, mas
// ainda 400 e ainda sem tocar o banco).
func TestHandleConfirmSocialPostPlatformRequiresURL(t *testing.T) {
	s := testServer(Config{})
	for _, body := range []string{"{}", `{"url":""}`, `{"url":"   "}`} {
		w := httptest.NewRecorder()
		s.handleConfirmSocialPostPlatform(w, socialPlatformReqBody("POST", validUUID, "instagram", body, 1))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%q code=%d", body, w.Code)
		}
		if !strings.Contains(w.Body.String(), "Cole o link da publicação antes de confirmar.") {
			t.Fatalf("body=%q resposta=%q", body, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.handleConfirmSocialPostPlatform(w, socialPlatformReqBody("POST", validUUID, "instagram", "xxx", 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo malformado: code=%d", w.Code)
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
		{"título longo demais", fmt.Sprintf(`{"title":"%s","platform":"instagram","pilar":"educacional","status":"ideia"}`, strings.Repeat("a", 201))},
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

// Corpo JSON malformado é rejeitado antes de buscar o post atual — não toca
// o banco (que é nil em testServer(Config{})).
func TestHandleUpdateSocialPostBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleUpdateSocialPost(w, socialReq("PUT", validUUID, "xxx", 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d", w.Code)
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

	w2 := httptest.NewRecorder()
	longContent := fmt.Sprintf(`{"content":"%s"}`, strings.Repeat("a", 4001))
	s.handleAddSocialPostNote(w2, socialReq("POST", validUUID, longContent, 1))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("conteúdo longo demais: code=%d", w2.Code)
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
//
// Mesma lacuna para o link obrigatório na confirmação (2026-09-23, ver
// TestHandleConfirmSocialPostPlatformRequiresURL acima pro que É testável):
//   - Confirmar com url válida → grava e aparece no GET /social/posts/{id} seguinte
//   - Reconfirmar com url diferente → sobrescreve (não duplica; UNIQUE(post_id,platform)
//     garante isso no banco, não dá pra observar sem um banco real)
//   - resolveSocialPostPublishConfirmations (social.go) populando publishConfirmations
//     em GET /social/posts (listagem) numa query só pra todos os posts da página
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

// --- Conta (de quem é a rede social do post) ---
//
// Mesma NOTA sobre cobertura de acima (harness sem Postgres real): só os
// ramos de validação que retornam antes de qualquer s.db são testáveis aqui.
// NÃO têm cobertura automatizada possível com este harness (registrado em
// PENDENCIAS.md):
//   - POST /social/contas com nome duplicado → 409 (constraint UNIQUE,
//     portalDBErr — insertSocialConta chama s.db.QueryRow)
//   - PUT /social/contas/{id} com id inexistente → 404 (updateSocialConta
//     chama s.db.QueryRow) e com nome duplicado → 409
//   - POST/PUT /social/posts sem contaId resolvendo para "Santos Tech"
//     (resolveDefaultContaID chama s.db.QueryRow)
//   - POST/PUT /social/posts com contaId inexistente → 400 (validateContaID
//     chama s.getSocialConta, que toca o banco)
//   - GET /social/contas → 200 com dados reais

func TestHandleCreateSocialContaBadBody(t *testing.T) {
	s := testServer(Config{})
	for _, body := range []string{"xxx", `{"nome":""}`, `{"nome":"   "}`} {
		w := httptest.NewRecorder()
		s.handleCreateSocialConta(w, httptest.NewRequest("POST", "/social/contas", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%q code=%d", body, w.Code)
		}
	}
}

func TestHandleUpdateSocialContaBadBody(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("PUT", "/social/contas/1", strings.NewReader(`{"nome":"","ativa":true}`))
	r.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	s.handleUpdateSocialConta(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("nome vazio: code=%d", w.Code)
	}
}

// id não-numérico é rejeitado antes de tocar o banco (mesma convenção de
// handleUpdateSocialSerie).
func TestHandleUpdateSocialContaBadID(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("PUT", "/social/contas/abc", strings.NewReader(`{"nome":"X","ativa":true}`))
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	s.handleUpdateSocialConta(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("id inválido: code=%d", w.Code)
	}
}
