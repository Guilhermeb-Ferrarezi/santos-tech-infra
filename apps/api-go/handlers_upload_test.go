package main

import (
	"net/http/httptest"
	"testing"
)

func TestHandleImageUploadDisabled(t *testing.T) {
	// Sem R2 configurado (s.r2 == nil) → 503 UPLOAD_DISABLED.
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleImageUpload(w, httptest.NewRequest("POST", "/auth/upload", nil))
	if w.Code != 503 {
		t.Fatalf("esperava 503 sem R2, veio %d", w.Code)
	}
}

func TestImageExt(t *testing.T) {
	cases := map[string]string{
		"image/png":  "png",
		"image/jpeg": "jpg",
		"image/webp": "webp",
		"image/gif":  "gif",
	}
	for ct, want := range cases {
		if got, ok := imageExt(ct); !ok || got != want {
			t.Errorf("imageExt(%q) = %q,%v; quer %q", ct, got, ok, want)
		}
	}
	if _, ok := imageExt("application/pdf"); ok {
		t.Error("pdf não deveria ser aceito")
	}
}
