package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// As rotas /portal/* exigem sessão (staff p/ leitura, admin p/ escrita). Sem
// token, o guard responde 401 antes de tocar o banco. Testamos os guards direto
// — como o resto da suíte (ver TestAuthGuard) — porque s.Routes() embrulha tudo
// no rate limit global, que depende do Redis (nil em testServer).
func TestPortalRoutesRequireAuthBeforeDB(t *testing.T) {
	s := testServer(Config{})

	for _, tc := range []struct {
		name   string
		h      http.HandlerFunc
		method string
		path   string
	}{
		{"overview", s.staffGuard(s.handlePortalOverview), "GET", "/portal/overview"},
		{"list courses", s.staffGuard(s.handlePortalListCourses), "GET", "/portal/courses"},
		{"create course", s.adminGuard(s.handlePortalCreateCourse), "POST", "/portal/courses"},
		{"list classes", s.staffGuard(s.handlePortalListClasses), "GET", "/portal/classes"},
		{"create class", s.adminGuard(s.handlePortalCreateClass), "POST", "/portal/classes"},
		{"list class rooms", s.staffGuard(s.handlePortalListClassRooms), "GET", "/portal/classes/123/rooms"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			tc.h(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}
