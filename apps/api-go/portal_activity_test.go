package main

import (
	"net/http/httptest"
	"testing"
)

func TestPortalActivityFiltersFromCapaOffset(t *testing.T) {
	r := httptest.NewRequest("GET", "/portal/activity-logs?offset=999999999", nil)
	f, err := portalActivityFiltersFrom(r)
	if err != nil {
		t.Fatalf("não esperava erro: %v", err)
	}
	if f.Offset != portalActivityMaxOffset {
		t.Fatalf("offset deveria ser limitado a %d, got %d", portalActivityMaxOffset, f.Offset)
	}
}
