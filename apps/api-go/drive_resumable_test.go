package main

// Testes do protocolo de upload resumable (drive_resumable.go): as funções
// PURAS (parseContentRange, parseGoogleRangeHeader, isValidDriveUploadID) sem
// nenhuma dependência externa, o DriveClient contra o fakeDriveClient de
// drive_test.go (nunca a API real do Google) e a serialização/persistência da
// sessão contra o Redis fake de testServerWithRedis (server_test.go).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseContentRange(t *testing.T) {
	ok := func(header string, start, end, total int64) {
		t.Helper()
		cr, ok := parseContentRange(header)
		if !ok {
			t.Fatalf("parseContentRange(%q) ok=false, esperava true", header)
		}
		if cr.Start != start || cr.End != end || cr.Total != total {
			t.Fatalf("parseContentRange(%q) = %+v, esperava {%d %d %d}", header, cr, start, end, total)
		}
	}
	bad := func(header string) {
		t.Helper()
		if _, ok := parseContentRange(header); ok {
			t.Fatalf("parseContentRange(%q) ok=true, esperava false", header)
		}
	}

	ok("bytes 0-1048575/2147483648", 0, 1048575, 2147483648)
	ok("bytes 1048576-2097151/2147483648", 1048576, 2097151, 2147483648)
	// último pedaço, termina exatamente no total-1
	ok("bytes 100-104/105", 100, 104, 105)

	bad("")
	bad("bytes 0-10")            // sem "/total"
	bad("bytes 0/10")            // sem "-" no range
	bad("bytes -1-10/20")        // start não numérico (parse falha por causa do "--")
	bad("bytes 10-5/20")         // end < start
	bad("bytes 0-20/20")         // total <= end
	bad("bytes abc-10/20")       // start não numérico
	bad("Bytes 0-10/20")         // prefixo com maiúscula não bate (case-sensitive, igual ao header real)
	bad("bytes 0-10/notanumber") // total não numérico
}

func TestParseGoogleRangeHeader(t *testing.T) {
	received, ok := parseGoogleRangeHeader("bytes=0-1048575")
	if !ok || received != 1048576 {
		t.Fatalf("received=%d ok=%v, esperava 1048576/true", received, ok)
	}
	if _, ok := parseGoogleRangeHeader(""); ok {
		t.Fatal("header vazio deveria dar ok=false (nenhum byte recebido ainda)")
	}
	if _, ok := parseGoogleRangeHeader("bytes 0-10"); ok {
		t.Fatal("sem o '=' (formato de resposta do Google) deveria dar ok=false")
	}
	if _, ok := parseGoogleRangeHeader("bytes=abc-10"); ok {
		t.Fatal("end não numérico deveria dar ok=false")
	}
}

func TestIsValidDriveUploadID(t *testing.T) {
	valid := randomToken(32) // 64 chars hex — a saída real de randomToken(32)
	if !isValidDriveUploadID(valid) {
		t.Errorf("isValidDriveUploadID(%q) = false, esperava true", valid)
	}
	invalid := []string{
		"",
		"abc",
		valid[:63],                             // curto demais
		valid + "a",                            // longo demais
		"zz" + valid[2:],                       // não é hex
		"3f9c2a10-1234-4abc-8def-0123456789ab", // um UUID, não um upload id
	}
	for _, v := range invalid {
		if isValidDriveUploadID(v) {
			t.Errorf("isValidDriveUploadID(%q) = true, esperava false", v)
		}
	}
}

// TestDriveUploadSessionRoundTrip cobre a serialização/persistência do
// registro no Redis: grava, lê de volta com os mesmos campos, apaga.
func TestDriveUploadSessionRoundTrip(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	sess := driveUploadSession{
		SessionURI: "https://upload.example/session/abc",
		Account:    driveAccountContratos,
		FolderID:   validUUID,
		Size:       123456,
		MimeType:   "video/mp4",
		Name:       "aula.mp4",
		UserID:     42,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
	}
	uploadID := randomToken(32)

	if err := s.saveDriveUploadSession(context.Background(), uploadID, sess); err != nil {
		t.Fatalf("saveDriveUploadSession: %v", err)
	}
	got, err := s.getDriveUploadSession(context.Background(), uploadID)
	if err != nil {
		t.Fatalf("getDriveUploadSession: %v", err)
	}
	if got == nil {
		t.Fatal("getDriveUploadSession devolveu nil logo depois de salvar")
	}
	if got.SessionURI != sess.SessionURI || got.Account != sess.Account || got.FolderID != sess.FolderID ||
		got.Size != sess.Size || got.MimeType != sess.MimeType || got.Name != sess.Name || got.UserID != sess.UserID {
		t.Fatalf("round-trip divergente: got=%+v want=%+v", got, sess)
	}

	s.deleteDriveUploadSession(uploadID)
	got2, err := s.getDriveUploadSession(context.Background(), uploadID)
	if err != nil {
		t.Fatalf("getDriveUploadSession após delete: %v", err)
	}
	if got2 != nil {
		t.Fatalf("esperava nil depois de deleteDriveUploadSession, veio %+v", got2)
	}
}

// TestGetDriveUploadSessionMiss: sessão inexistente devolve nil, sem erro —
// mesma convenção de getDriveFolder.
func TestGetDriveUploadSessionMiss(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	got, err := s.getDriveUploadSession(context.Background(), randomToken(32))
	if err != nil {
		t.Fatalf("err = %v, esperava nil", err)
	}
	if got != nil {
		t.Fatalf("esperava nil pra sessão inexistente, veio %+v", got)
	}
}

// ── DriveClient contra o fakeDriveClient (drive_test.go) ────────────────────

func TestStartResumableUploadAs(t *testing.T) {
	var gotBody map[string]any
	var gotHeaders http.Header
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("método = %s, esperava POST", r.Method)
		}
		gotHeaders = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Location", "https://upload.example/session/xyz")
		w.WriteHeader(http.StatusOK)
	})

	uri, err := d.StartResumableUploadAs(context.Background(), "", "parent123", "aula.mp4", "video/mp4", 999)
	if err != nil {
		t.Fatalf("StartResumableUploadAs: %v", err)
	}
	if uri != "https://upload.example/session/xyz" {
		t.Fatalf("uri = %q", uri)
	}
	if gotBody["name"] != "aula.mp4" {
		t.Fatalf("name enviado = %v", gotBody["name"])
	}
	parents, _ := gotBody["parents"].([]any)
	if len(parents) != 1 || parents[0] != "parent123" {
		t.Fatalf("parents enviado = %v", gotBody["parents"])
	}
	if gotHeaders.Get("X-Upload-Content-Type") != "video/mp4" {
		t.Fatalf("X-Upload-Content-Type = %q", gotHeaders.Get("X-Upload-Content-Type"))
	}
	if gotHeaders.Get("X-Upload-Content-Length") != "999" {
		t.Fatalf("X-Upload-Content-Length = %q", gotHeaders.Get("X-Upload-Content-Length"))
	}
}

func TestStartResumableUploadAsSemLocation(t *testing.T) {
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // 200 mas esqueceu o header Location
	})
	if _, err := d.StartResumableUploadAs(context.Background(), "", "parent123", "a.mp4", "video/mp4", 1); err == nil {
		t.Fatal("esperava erro quando a resposta não tem header Location")
	}
}

// TestPutResumableChunk308 cobre o "continue mandando": Google devolve 308
// com o header Range dizendo até onde recebeu.
func TestPutResumableChunk308(t *testing.T) {
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Range"); got != "bytes 0-99/1000" {
			t.Fatalf("Content-Range = %q", got)
		}
		w.Header().Set("Range", "bytes=0-99")
		w.WriteHeader(308)
	})
	result, err := d.PutResumableChunk(context.Background(), "", "https://upload.example/s", 0, 99, 1000, http.NoBody)
	if err != nil {
		t.Fatalf("PutResumableChunk: %v", err)
	}
	if result.Done || result.Received != 100 {
		t.Fatalf("result = %+v, esperava {Done:false Received:100}", result)
	}
}

// TestPutResumableChunkFinalizaUpload cobre o último pedaço: 200/201 com o
// JSON do arquivo pronto.
func TestPutResumableChunkFinalizaUpload(t *testing.T) {
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "f1", "name": "aula.mp4", "mimeType": "video/mp4"})
	})
	result, err := d.PutResumableChunk(context.Background(), "", "https://upload.example/s", 900, 999, 1000, http.NoBody)
	if err != nil {
		t.Fatalf("PutResumableChunk: %v", err)
	}
	if !result.Done || result.File.ID != "f1" {
		t.Fatalf("result = %+v", result)
	}
}

// TestPutResumableChunkSessaoPerdida cobre 404/410 → errResumableSessionLost.
func TestPutResumableChunkSessaoPerdida(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		})
		_, err := d.PutResumableChunk(context.Background(), "", "https://upload.example/s", 0, 9, 100, http.NoBody)
		if err == nil {
			t.Fatalf("status %d: esperava erro", status)
		}
		if err != errResumableSessionLost {
			t.Fatalf("status %d: err = %v, esperava errResumableSessionLost", status, err)
		}
	}
}

// TestPutResumableChunkNaoRepete: corpo vem de r.Body (io.Reader genérico,
// sem GetBody) — d.do NÃO pode tentar de novo num 5xx, senão reenviaria um
// pedaço que já foi parcialmente lido/consumido.
func TestPutResumableChunkNaoRepete(t *testing.T) {
	var calls int
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	})
	// io.NopCloser: um tipo concreto QUALQUER que não seja *bytes.Buffer/
	// *bytes.Reader/*strings.Reader — são esses três casos especiais que o
	// próprio net/http reconhece e preenche GetBody sozinho (replayable). Um
	// r.Body de verdade (vindo da conexão HTTP) nunca é nenhum desses três.
	body := io.NopCloser(strings.NewReader("conteudo-do-pedaco"))
	_, err := d.PutResumableChunk(context.Background(), "", "https://upload.example/s", 0, 17, 18, body)
	if err == nil {
		t.Fatal("esperava erro em 500")
	}
	if calls != 1 {
		t.Fatalf("%d tentativas, esperava 1 (corpo não é replayable)", calls)
	}
}

// TestGetResumableStatusRepeteEmErroTransitorio: ao contrário do PUT de
// pedaço, a consulta de status vai com body nil — CONTINUA repetível em
// 429/5xx via d.do (ver comentário em GetResumableStatus).
func TestGetResumableStatusRepeteEmErroTransitorio(t *testing.T) {
	var calls int
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Content-Range"); got != "bytes */1000" {
			t.Fatalf("Content-Range = %q, esperava \"bytes */1000\"", got)
		}
		if calls < driveMaxAttempts {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Range", "bytes=0-499")
		w.WriteHeader(308)
	})
	result, err := d.GetResumableStatus(context.Background(), "", "https://upload.example/s", 1000)
	if err != nil {
		t.Fatalf("GetResumableStatus: %v", err)
	}
	if result.Done || result.Received != 500 {
		t.Fatalf("result = %+v", result)
	}
	if calls != driveMaxAttempts {
		t.Fatalf("%d tentativas, esperava %d (deveria repetir em 429)", calls, driveMaxAttempts)
	}
}
