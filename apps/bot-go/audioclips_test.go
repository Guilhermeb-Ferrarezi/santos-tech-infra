package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Sem AUDIO_CLIPS_DIR o store fica inerte: nada de pânico, nada de erro no boot,
// e o bot segue no comportamento anterior.
func TestAudioClipStoreDesabilitado(t *testing.T) {
	var nilStore *AudioClipStore
	if nilStore.Enabled() {
		t.Fatal("store nil deveria estar desabilitado")
	}
	if (&AudioClipStore{}).Enabled() {
		t.Fatal("store sem dir nem pool deveria estar desabilitado")
	}
	if (&AudioClipStore{dir: "/tmp"}).Enabled() {
		t.Fatal("store sem pool deveria estar desabilitado")
	}
	// Store desabilitado recusa leitura em vez de tentar ler do disco.
	if _, err := (&AudioClipStore{dir: "/tmp"}).Read(&AudioClip{FilePath: "x.ogg"}); err == nil {
		t.Fatal("store desabilitado deveria recusar Read")
	}
	if _, err := (&AudioClipStore{dir: "/tmp"}).Read(nil); err == nil {
		t.Fatal("clipe nulo deveria virar erro")
	}
}

// file_path vem do banco. Se alguém conseguir gravar "../../etc/passwd" ali,
// a leitura não pode escapar do diretório de áudios dentro do container.
func TestResolveClipPathRecusaEscape(t *testing.T) {
	dir := t.TempDir()

	ok := []string{
		"henrique/saudacao/saud_bomdia__v1.ogg",
		"arquivo.ogg",
		"./henrique/x.ogg",
		"henrique/../henrique/x.ogg", // sobe e volta, mas continua dentro
	}
	for _, p := range ok {
		if _, err := resolveClipPath(dir, p); err != nil {
			t.Errorf("caminho legítimo recusado: %q → %v", p, err)
		}
	}

	escapes := []string{
		"../segredo.txt",
		"../../etc/passwd",
		"henrique/../../fora.ogg",
		"henrique/../../../../../../etc/shadow",
	}
	for _, p := range escapes {
		if _, err := resolveClipPath(dir, p); err == nil {
			t.Errorf("ESCAPE NÃO BLOQUEADO: %q", p)
		}
	}
}

// Leitura feliz: arquivo dentro do diretório volta com o conteúdo.
func TestReadClipFrom(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "henrique", "saudacao")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	alvo := filepath.Join(sub, "saud_bomdia__v1.ogg")
	if err := os.WriteFile(alvo, []byte("OggS-conteudo"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readClipFrom(dir, "henrique/saudacao/saud_bomdia__v1.ogg")
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if string(got) != "OggS-conteudo" {
		t.Fatalf("conteúdo = %q", got)
	}
}

// Arquivo vazio é falha: sem isso o bot enviaria uma nota de voz quebrada.
func TestReadClipFromArquivoVazio(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vazio.ogg"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readClipFrom(dir, "vazio.ogg"); err == nil {
		t.Fatal("arquivo vazio deveria virar erro")
	}
}

// Arquivo inexistente também é erro — e não pânico.
func TestReadClipFromInexistente(t *testing.T) {
	if _, err := readClipFrom(t.TempDir(), "nao-existe.ogg"); err == nil {
		t.Fatal("arquivo inexistente deveria virar erro")
	}
}

func TestTruncaRunesNaoQuebraUTF8(t *testing.T) {
	// "ção" repetido: quase todo caractere ocupa 2 bytes, então o corte em 500
	// bytes cairia no meio de um deles.
	s := strings.Repeat("ção ", 400)
	got := truncaRunes(s, 500)

	if !utf8.ValidString(got) {
		t.Fatalf("truncaRunes devolveu UTF-8 inválido")
	}
	if n := utf8.RuneCountInString(got); n != 500 {
		t.Fatalf("esperava 500 runes, veio %d", n)
	}
	if s2 := truncaRunes("curto", 500); s2 != "curto" {
		t.Fatalf("texto curto foi alterado: %q", s2)
	}
	if s2 := truncaRunes("", 500); s2 != "" {
		t.Fatalf("string vazia virou %q", s2)
	}
}
