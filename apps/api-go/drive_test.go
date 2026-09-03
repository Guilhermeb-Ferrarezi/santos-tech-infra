package main

// Testes do DriveClient contra um servidor HTTP fake local (nunca a API real
// do Google) — um redirectTransport reescreve qualquer request pra apontar
// pro httptest.Server, então o código de produção (que usa URLs fixas
// https://www.googleapis.com/...) não precisa saber que está em teste.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"testing"
)

type redirectTransport struct {
	base *url.URL
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = t.base.Scheme
	req.URL.Host = t.base.Host
	return http.DefaultTransport.RoundTrip(req)
}

func fakeDriveClient(t *testing.T, handler http.HandlerFunc) *DriveClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cli := &http.Client{Transport: &redirectTransport{base: base}}
	return &DriveClient{http: cli, stream: cli}
}

// TestDriveDoRetryTransitorio: 429 e 5xx do Drive são transitórios (o Google
// rate-limita com alguma frequência) — antes qualquer status != 200 subia como
// erro na primeira tentativa.
func TestDriveDoRetryTransitorio(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		var calls int
		d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls < driveMaxAttempts {
				w.WriteHeader(status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{{"id": "f1", "name": "a.txt"}}})
		})
		files, err := d.ListFiles(context.Background(), "root")
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if len(files) != 1 {
			t.Errorf("status %d: %d arquivos, quer 1", status, len(files))
		}
		if calls != driveMaxAttempts {
			t.Errorf("status %d: %d tentativas, quer %d", status, calls, driveMaxAttempts)
		}
	}
}

// TestDriveDoNaoRepeteErroDefinitivo: 4xx (fora 429) é definitivo — repetir só
// gasta quota da service account.
func TestDriveDoNaoRepeteErroDefinitivo(t *testing.T) {
	var calls int
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := d.ListFiles(context.Background(), "root"); err == nil {
		t.Fatal("esperava erro em 403")
	}
	if calls != 1 {
		t.Errorf("%d tentativas em 403, quer 1", calls)
	}
}

// TestDriveDoDesisteAposMaxAttempts garante que o retry tem fim.
func TestDriveDoDesisteAposMaxAttempts(t *testing.T) {
	var calls int
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := d.ListFiles(context.Background(), "root"); err == nil {
		t.Fatal("esperava erro depois de esgotar as tentativas")
	}
	if calls != driveMaxAttempts {
		t.Errorf("%d tentativas, quer %d", calls, driveMaxAttempts)
	}
}

// TestListFilesFollowsPagination cobre a paginação adicionada em ListFiles —
// antes ela só lia a primeira página (pageSize=200) e devolvia isso como se
// fosse a lista completa, perdendo arquivos silenciosamente numa pasta com
// mais de 200 itens.
func TestListFilesFollowsPagination(t *testing.T) {
	var calls int
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		pageToken := r.URL.Query().Get("pageToken")
		w.Header().Set("Content-Type", "application/json")
		switch pageToken {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"nextPageToken": "page2",
				"files":         []map[string]any{{"id": "f1", "name": "a.txt", "mimeType": "text/plain"}},
			})
		case "page2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{"id": "f2", "name": "b.txt", "mimeType": "text/plain"}},
			})
		default:
			t.Fatalf("pageToken inesperado: %q", pageToken)
		}
	})

	files, err := d.ListFiles(context.Background(), "root123")
	if err != nil {
		t.Fatalf("ListFiles err: %v", err)
	}
	if calls != 2 {
		t.Fatalf("esperava 2 chamadas HTTP (uma por página), veio %d", calls)
	}
	if len(files) != 2 {
		t.Fatalf("esperava 2 arquivos agregados das 2 páginas, veio %d", len(files))
	}
	if files[0].ID != "f1" || files[1].ID != "f2" {
		t.Fatalf("conteúdo/ordem inesperado: %+v", files)
	}
}

// TestListFilesStopsAtPageCap garante que um Drive que nunca para de mandar
// nextPageToken (bug do lado de lá, ou pasta anormalmente grande) não trava
// ListFiles pra sempre — o teto (driveListFilesMaxPages) corta e devolve o
// que já tiver, em vez de um loop sem fim.
func TestListFilesStopsAtPageCap(t *testing.T) {
	var calls int
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nextPageToken": "sempre-tem-mais",
			"files":         []map[string]any{{"id": "f", "name": "x.txt", "mimeType": "text/plain"}},
		})
	})

	files, err := d.ListFiles(context.Background(), "root123")
	if err != nil {
		t.Fatalf("ListFiles err: %v", err)
	}
	if calls != driveListFilesMaxPages {
		t.Fatalf("esperava parar em %d chamadas (o teto), fez %d", driveListFilesMaxPages, calls)
	}
	if len(files) != driveListFilesMaxPages {
		t.Fatalf("esperava %d arquivos (1 por página até o teto), veio %d", driveListFilesMaxPages, len(files))
	}
}

// TestEnsureFileInFolderCachesDescendantCheck cobre o cache Redis adicionado
// em ensureFileInFolder — chamar a mesma checagem de ancestralidade repetidas
// vezes (o caso comum: download+rename+thumbnail do mesmo arquivo em
// sequência) não deveria bater no Drive de novo a cada vez.
func TestEnsureFileInFolderCachesDescendantCheck(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	var calls int
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"parents": []string{"root123"}})
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}

	for i := 0; i < 3; i++ {
		if err := s.ensureFileInFolder(context.Background(), folder, "sub456", true); err != nil {
			t.Fatalf("chamada %d: err inesperado: %v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("esperava 1 chamada HTTP real ao Drive (as outras 2 deviam vir do cache), veio %d", calls)
	}
}

func TestCreateFolder(t *testing.T) {
	var gotBody map[string]any
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("método = %s, esperava POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "newfolder1", "name": gotBody["name"], "mimeType": driveFolderMimeType})
	})

	f, err := d.CreateFolder(context.Background(), "parent123", "Nova pasta")
	if err != nil {
		t.Fatalf("CreateFolder err: %v", err)
	}
	if f.ID != "newfolder1" || f.Name != "Nova pasta" || f.MimeType != driveFolderMimeType {
		t.Fatalf("resultado inesperado: %+v", f)
	}
	if gotBody["mimeType"] != driveFolderMimeType {
		t.Fatalf("mimeType enviado = %v, esperava %q", gotBody["mimeType"], driveFolderMimeType)
	}
	parents, _ := gotBody["parents"].([]any)
	if len(parents) != 1 || parents[0] != "parent123" {
		t.Fatalf("parents enviado = %v, esperava [\"parent123\"]", gotBody["parents"])
	}
}

// TestMoveFile cobre o fluxo de duas chamadas (getParents, depois PATCH com
// addParents/removeParents) — o Drive não tem um "parent" único mutável, tem
// que listar explicitamente quem sai e quem entra.
func TestMoveFile(t *testing.T) {
	var patchCalled bool
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"parents": []string{"oldParent1"}})
		case http.MethodPatch:
			patchCalled = true
			if got := r.URL.Query().Get("addParents"); got != "newParent1" {
				t.Fatalf("addParents = %q, esperava newParent1", got)
			}
			if got := r.URL.Query().Get("removeParents"); got != "oldParent1" {
				t.Fatalf("removeParents = %q, esperava oldParent1", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "f1", "name": "movido.txt", "mimeType": "text/plain"})
		default:
			t.Fatalf("método inesperado: %s", r.Method)
		}
	})

	f, err := d.MoveFile(context.Background(), "f1", "newParent1")
	if err != nil {
		t.Fatalf("MoveFile err: %v", err)
	}
	if !patchCalled {
		t.Fatal("esperava um PATCH depois do GET de parents")
	}
	if f.ID != "f1" {
		t.Fatalf("resultado inesperado: %+v", f)
	}
}

// TestIsDescendantErroTransitorioNaoViraNegado: antes, qualquer status != 200 na
// consulta de `parents` era tratado como "não é ancestral" — um 429 do Google
// (rate limit da service account) virava 403 "acesso negado", e o false ainda
// era cacheado por 5 minutos.
func TestIsDescendantErroTransitorioNaoViraNegado(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		})
		ok, err := d.IsDescendant(context.Background(), "filho", "raiz")
		if err == nil {
			t.Errorf("status %d: err = nil, quer erro (senão vira acesso negado)", status)
		}
		if ok {
			t.Errorf("status %d: ok = true num erro", status)
		}
	}
}

// TestIsDescendantItemSumidoNaoEhErro: 404/403 num dos pais é definitivo (item
// na lixeira, ou a service account não enxerga) — não conta como ancestral, mas
// não pode abortar a busca pelos outros pais.
func TestIsDescendantItemSumidoNaoEhErro(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		})
		ok, err := d.IsDescendant(context.Background(), "filho", "raiz")
		if err != nil {
			t.Errorf("status %d: err = %v, quer nil", status, err)
		}
		if ok {
			t.Errorf("status %d: ok = true", status)
		}
	}
}

// TestIsDescendantAchaAncestral cobre o caminho feliz de vários níveis.
func TestIsDescendantAchaAncestral(t *testing.T) {
	parents := map[string][]string{"neto": {"filho"}, "filho": {"raiz"}}
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"parents": parents[id]})
	})
	ok, err := d.IsDescendant(context.Background(), "neto", "raiz")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !ok {
		t.Error("neto deveria ser descendente de raiz")
	}
}

// panicReader simula um bug de runtime no meio do stream de upload (ex.: um
// io.Reader de terceiros mal comportado explodindo no meio da leitura).
type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic("boom")
}

// TestUploadFileRecoversPanicNoPipe: um panic dentro da goroutine que
// alimenta o io.Pipe do multipart (ex.: r.Read explodindo) tem que virar um
// erro comum devolvido por UploadFile, não derrubar o processo inteiro — sem
// o recover() em UploadFile, este teste crasha o binário de teste em vez de
// simplesmente falhar.
func TestUploadFileRecoversPanicNoPipe(t *testing.T) {
	d := fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := d.UploadFile(context.Background(), "folder1", "a.txt", "text/plain", panicReader{})
	if err == nil {
		t.Fatal("esperava erro (panic recuperado), got nil")
	}
}
