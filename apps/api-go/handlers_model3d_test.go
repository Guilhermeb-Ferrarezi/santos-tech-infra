package main

import (
	"bytes"
	"encoding/binary"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func validGLB() []byte {
	// header: magic "glTF" + version(uint32=2) + total length(uint32) + um chunk
	// JSON mínimo (chunk type "JSON") pra fechar o tamanho total corretamente.
	chunkData := []byte(`{"asset":{"version":"2.0"}}`)
	for len(chunkData)%4 != 0 {
		chunkData = append(chunkData, ' ')
	}
	chunkHeader := make([]byte, 8)
	binary.LittleEndian.PutUint32(chunkHeader[0:4], uint32(len(chunkData)))
	copy(chunkHeader[4:8], "JSON")

	total := 12 + 8 + len(chunkData)
	header := make([]byte, 12)
	copy(header[0:4], "glTF")
	binary.LittleEndian.PutUint32(header[4:8], 2)
	binary.LittleEndian.PutUint32(header[8:12], uint32(total))

	out := append([]byte{}, header...)
	out = append(out, chunkHeader...)
	out = append(out, chunkData...)
	return out
}

func validBinarySTL(triangleCount int) []byte {
	out := make([]byte, 80)
	countBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBytes, uint32(triangleCount))
	out = append(out, countBytes...)
	out = append(out, make([]byte, triangleCount*50)...)
	return out
}

func TestValidateModel3DContent(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		ext  string
		ok   bool
	}{
		{"glb válido", validGLB(), "glb", true},
		{"glb com magic errado", []byte("não é glTF nem perto"), "glb", false},
		{"gltf válido", []byte(`{"asset":{"version":"2.0"},"scenes":[]}`), "gltf", true},
		{"gltf sem asset", []byte(`{"scenes":[]}`), "gltf", false},
		{"gltf não é json", []byte("<html>não é json</html>"), "gltf", false},
		{"stl binário válido", validBinarySTL(2), "stl", true},
		{"stl ascii válido", []byte("solid cubo\nfacet normal 0 0 0\nendsolid cubo\n"), "stl", true},
		{"stl lixo", []byte("isso não é stl de jeito nenhum"), "stl", false},
		{"obj válido", []byte("# comentário\nv 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"), "obj", true},
		{"obj sem face", []byte("v 0 0 0\nv 1 0 0\n"), "obj", false},
		{"fbx binário válido", append([]byte("Kaydara FBX Binary"), 0, 0x1a, 0), "fbx", true},
		{"fbx ascii válido", []byte("; FBX 7.7.0 project file\n"), "fbx", true},
		{"fbx lixo", []byte("não é fbx"), "fbx", false},
		{"blend válido", append([]byte("BLENDER"), []byte("-v300")...), "blend", true},
		{"blend gzip válido", []byte{0x1f, 0x8b, 0x08, 0x00}, "blend", true},
		{"blend lixo", []byte("não é blend"), "blend", false},
		{"extensão desconhecida", []byte("qualquer coisa"), "exe", false},
		{"extensão certa, conteúdo de outro formato (glb declarado, stl de verdade)", validBinarySTL(1), "glb", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ext, ct, ok := validateModel3DContent(c.data, c.ext)
			if ok != c.ok {
				t.Fatalf("ok=%v (queria %v)", ok, c.ok)
			}
			if ok && (ext == "" || ct == "") {
				t.Fatalf("esperava ext/contentType não vazios, ext=%q ct=%q", ext, ct)
			}
		})
	}
}

func TestHandleUploadModel3DDisabled(t *testing.T) {
	s := testServer(Config{}) // sem R2 configurado → s.r2 é nil

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, _ := mw.CreateFormFile("file", "modelo.glb")
	part.Write(validGLB())
	mw.Close()

	req := httptest.NewRequest("POST", "/auth/admin/models3d", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.handleUploadModel3D(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d (queria 503 UPLOAD_DISABLED)", w.Code)
	}
}

func TestHandleUploadModel3DMissingFile(t *testing.T) {
	s := &Server{r2: &R2{}} // r2 não-nil só pra passar do guard inicial

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	mw.WriteField("not_file", "x")
	mw.Close()

	req := httptest.NewRequest("POST", "/auth/admin/models3d", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.handleUploadModel3D(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

func TestHandleUploadModel3DInvalidContent(t *testing.T) {
	s := &Server{r2: &R2{}}

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, _ := mw.CreateFormFile("file", "modelo.glb")
	part.Write([]byte("isso não é um glb de verdade"))
	mw.Close()

	req := httptest.NewRequest("POST", "/auth/admin/models3d", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.handleUploadModel3D(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400 VALIDATION_ERROR)", w.Code)
	}
}

func TestHandleDeleteModel3DInvalidID(t *testing.T) {
	s := &Server{r2: &R2{}}
	req := httptest.NewRequest("DELETE", "/auth/admin/models3d/abc", strings.NewReader(""))
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	s.handleDeleteModel3D(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400 INVALID_ID)", w.Code)
	}
}

func TestHandleDeleteModel3DDisabled(t *testing.T) {
	s := testServer(Config{})
	req := httptest.NewRequest("DELETE", "/auth/admin/models3d/1", strings.NewReader(""))
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	s.handleDeleteModel3D(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d (queria 503 UPLOAD_DISABLED)", w.Code)
	}
}
