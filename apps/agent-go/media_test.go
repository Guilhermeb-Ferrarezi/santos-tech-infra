package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateAttachments(t *testing.T) {
	small := base64.StdEncoding.EncodeToString([]byte("abc"))
	ok := []Attachment{{MediaType: "image/jpeg", Data: small}, {MediaType: "application/pdf", Data: small}}
	if err := validateAttachments(ok); err != nil {
		t.Fatalf("anexos válidos rejeitados: %v", err)
	}
	if err := validateAttachments([]Attachment{{MediaType: "text/html", Data: small}}); err == nil {
		t.Fatal("mimetype não suportado deveria falhar")
	}
	big := strings.Repeat("A", (maxAttachmentBytes/3+2)*4) // base64 de >5MB
	if err := validateAttachments([]Attachment{{MediaType: "image/png", Data: big}}); err == nil {
		t.Fatal("anexo >5MB deveria falhar")
	}
	five := make([]Attachment, 5)
	for i := range five {
		five[i] = Attachment{MediaType: "image/png", Data: small}
	}
	if err := validateAttachments(five); err == nil {
		t.Fatal("mais de 4 anexos deveria falhar")
	}
}

func TestMediaPromptNote(t *testing.T) {
	if mediaPromptNote(nil) != "" {
		t.Fatal("sem anexos deve ser vazio")
	}
	note := mediaPromptNote([]string{"/w/media/a.jpg"})
	if !strings.Contains(note, "/w/media/a.jpg") || !strings.Contains(note, "Read") {
		t.Fatalf("nota deve citar o path e afirmar a tool Read: %q", note)
	}
}

func TestMediaMarkers(t *testing.T) {
	m := mediaMarkers([]Attachment{{MediaType: "image/png"}, {MediaType: "application/pdf"}})
	if m != " [imagem] [pdf]" {
		t.Fatalf("markers errados: %q", m)
	}
}

func TestSaveAttachments(t *testing.T) {
	dir := t.TempDir()
	data := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47})
	paths, err := saveAttachments(dir, []Attachment{{MediaType: "image/png", Data: data}})
	if err != nil {
		t.Fatalf("saveAttachments falhou: %v", err)
	}
	if len(paths) != 1 || !strings.HasPrefix(paths[0], dir+"/media/") || !strings.HasSuffix(paths[0], ".png") {
		t.Fatalf("path inesperado: %v", paths)
	}
	if _, err := saveAttachments(dir, []Attachment{{MediaType: "image/png", Data: "%%%"}}); err == nil {
		t.Fatal("base64 inválido deveria falhar")
	}
}
