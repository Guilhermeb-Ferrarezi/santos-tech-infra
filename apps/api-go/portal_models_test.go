package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPortalParsePositiveID(t *testing.T) {
	r := httptest.NewRequest("GET", "/portal/courses/12", nil)
	r.SetPathValue("courseId", "12")
	id, err := portalPathID(r, "courseId")
	if err != nil || id != 12 {
		t.Fatalf("id=%d err=%v", id, err)
	}

	r2 := httptest.NewRequest("GET", "/portal/courses/x", nil)
	r2.SetPathValue("courseId", "x")
	if _, err := portalPathID(r2, "courseId"); err == nil {
		t.Fatal("expected invalid id error")
	}
}

func TestPortalPagination(t *testing.T) {
	r := httptest.NewRequest("GET", "/portal/courses?page=0&limit=999&q=%20informatica%20", nil)
	p := portalPaginationFrom(r)
	if p.Page != 1 || p.Limit != 100 || p.Offset != 0 || p.Query != "informatica" {
		t.Fatalf("pagination=%+v", p)
	}
}

// TestPortalBodyJSONSizeLimit garante que portalBodyJSON rejeita payloads maiores
// que 64 KiB, espelhando o comportamento dos handlers de auth (http.MaxBytesReader).
func TestPortalBodyJSONSizeLimit(t *testing.T) {
	large := `{"name":"` + strings.Repeat("a", 65*1024) + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/portal/courses", strings.NewReader(large))
	var in portalCourseInput
	if err := portalBodyJSON(w, r, &in); err == nil {
		t.Fatal("body maior que 64 KiB deve retornar erro")
	}
}

func TestPortalCreateCourseValidation(t *testing.T) {
	var in portalCourseInput
	if err := decodePortalJSON(strings.NewReader(`{"name":"A"}`), &in); err != nil {
		t.Fatal(err)
	}
	if err := in.validateCreate(); err == nil {
		t.Fatal("expected short name validation")
	}

	var ok portalCourseInput
	if err := decodePortalJSON(strings.NewReader(`{"name":"Informática","isPaid":true,"durationHours":12,"price":"100"}`), &ok); err != nil {
		t.Fatal(err)
	}
	if err := ok.validateCreate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
