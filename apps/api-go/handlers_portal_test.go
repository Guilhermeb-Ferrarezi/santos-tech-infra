package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestPortalCatalogBadIDsBeforeDB(t *testing.T) {
	s := testServer(Config{})

	cases := []struct {
		name string
		fn   http.HandlerFunc
		key  string
		val  string
		path string
	}{
		{"course", s.handlePortalGetCourse, "courseId", "x", "/portal/courses/x"},
		{"modules", s.handlePortalListModules, "courseId", "0", "/portal/courses/0/modules"},
		{"phases", s.handlePortalListPhases, "moduleId", "-1", "/portal/modules/-1/phases"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.path, nil)
			r.SetPathValue(tc.key, tc.val)
			w := httptest.NewRecorder()
			tc.fn(w, reqAs(r, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestPortalCatalogMutationValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})

	w := httptest.NewRecorder()
	s.handlePortalCreateCourse(w, reqAs(httptest.NewRequest("POST", "/portal/courses", strings.NewReader(`{"name":"A"}`)), 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("short course name code=%d", w.Code)
	}

	r := httptest.NewRequest("PATCH", "/portal/courses/x", strings.NewReader(`{"name":"Curso"}`))
	r.SetPathValue("courseId", "x")
	w2 := httptest.NewRecorder()
	s.handlePortalUpdateCourse(w2, reqAs(r, 1))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("bad course id code=%d", w2.Code)
	}

	r3 := httptest.NewRequest("POST", "/portal/courses/1/modules", strings.NewReader(`{"name":"M"}`))
	r3.SetPathValue("courseId", "1")
	w3 := httptest.NewRecorder()
	s.handlePortalCreateModule(w3, reqAs(r3, 1))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("short module name code=%d", w3.Code)
	}

	r4 := httptest.NewRequest("POST", "/portal/modules/1/phases", strings.NewReader(`{"name":"F"}`))
	r4.SetPathValue("moduleId", "1")
	w4 := httptest.NewRecorder()
	s.handlePortalCreatePhase(w4, reqAs(r4, 1))
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("short phase name code=%d", w4.Code)
	}
}
