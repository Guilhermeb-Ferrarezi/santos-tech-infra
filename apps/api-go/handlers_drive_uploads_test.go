package main

// Testes dos handlers de upload resumable. Mesma restrição do resto do
// pacote (sem Postgres/Redis real no CI — ver handlers_drive_folders_test.go):
// os testes de DRIVE_DISABLED batem no handler completo (retornam antes de
// tocar no banco); os demais chamam o miolo testável (createDriveUpload/
// putDriveUploadChunk/getDriveUploadStatus) com um *DriveFolder montado na
// mão, igual ensureFileInFolder/checkDriveFolderNesting fazem em
// handlers_drive_folders_test.go — evita depender de Postgres pra resolver
// {id} via getDriveFolder. O Redis é real (miniredis via testServerWithRedis),
// já que a sessão do upload MORA lá (não é um cache-aside opcional).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestHandleCreateDriveUploadDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	w := httptest.NewRecorder()
	s.handleCreateDriveUpload(w, driveFolderReq("POST", validUUID, `{"name":"a.mp4","size":100}`, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandlePutDriveUploadChunkDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	r := httptest.NewRequest("PUT", "/drive-folders/"+validUUID+"/uploads/xyz", strings.NewReader("dados"))
	r.SetPathValue("id", validUUID)
	r.SetPathValue("uploadId", "xyz")
	w := httptest.NewRecorder()
	s.handlePutDriveUploadChunk(w, reqAs(r, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleGetDriveUploadStatusDriveDisabled(t *testing.T) {
	s := testServer(Config{}) // s.drive == nil
	r := httptest.NewRequest("GET", "/drive-folders/"+validUUID+"/uploads/xyz", nil)
	r.SetPathValue("id", validUUID)
	r.SetPathValue("uploadId", "xyz")
	w := httptest.NewRecorder()
	s.handleGetDriveUploadStatus(w, reqAs(r, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", w.Code)
	}
}

// ── createDriveUpload (miolo) ────────────────────────────────────────────

func TestDriveUploadInputValidate(t *testing.T) {
	bad := []driveUploadInput{
		{Name: "  ", Size: 100},                          // nome vazio
		{Name: strings.Repeat("a", 256), Size: 100},      // nome longo demais
		{Name: "a.mp4", Size: 0},                         // tamanho zero
		{Name: "a.mp4", Size: -1},                        // tamanho negativo
		{Name: "a.mp4", Size: maxDriveResumableSize + 1}, // acima do teto
	}
	for i, in := range bad {
		if err := in.validate(); err == nil {
			t.Errorf("caso %d (%+v): esperava erro", i, in)
		}
	}
	ok := driveUploadInput{Name: "aula.mp4", Size: maxDriveResumableSize}
	if err := ok.validate(); err != nil {
		t.Errorf("caso válido no limite exato: err = %v", err)
	}
}

func TestCreateDriveUploadValidation(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("não deveria chegar a chamar o Drive com corpo inválido")
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}

	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/drive-folders/"+validUUID+"/uploads", strings.NewReader(`{"name":"  ","size":100}`)), 1)
	s.createDriveUpload(w, r, folder)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("nome vazio: code=%d", w.Code)
	}

	w2 := httptest.NewRecorder()
	r2 := reqAs(httptest.NewRequest("POST", "/drive-folders/"+validUUID+"/uploads", strings.NewReader(`{"name":"a.mp4","size":0}`)), 1)
	s.createDriveUpload(w2, r2, folder)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("tamanho zero: code=%d", w2.Code)
	}
}

// TestCreateDriveUploadSucesso cobre o caminho feliz: sessão aberta no
// (fake) Drive, uploadId devolvido e sessão gravada no Redis com os campos
// certos.
func TestCreateDriveUploadSucesso(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://upload.example/session/abc")
		w.WriteHeader(http.StatusOK)
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123", UploadAccount: driveAccountContratos}

	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/drive-folders/"+validUUID+"/uploads", strings.NewReader(`{"name":"aula.mp4","mimeType":"video/mp4","size":5000000000}`)), 42)
	s.createDriveUpload(w, r, folder)
	if w.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		UploadID  string `json:"uploadId"`
		ChunkSize int64  `json:"chunkSize"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	if !isValidDriveUploadID(out.UploadID) {
		t.Fatalf("uploadId mal formado: %q", out.UploadID)
	}
	if out.ChunkSize != driveResumableChunkSize {
		t.Fatalf("chunkSize = %d, esperava %d", out.ChunkSize, driveResumableChunkSize)
	}

	sess, err := s.getDriveUploadSession(context.Background(), out.UploadID)
	if err != nil || sess == nil {
		t.Fatalf("sessão não foi salva: sess=%v err=%v", sess, err)
	}
	if sess.SessionURI != "https://upload.example/session/abc" || sess.Account != driveAccountContratos ||
		sess.FolderID != validUUID || sess.UserID != 42 || sess.Size != 5000000000 || sess.MimeType != "video/mp4" {
		t.Fatalf("sessão salva com campos errados: %+v", sess)
	}
}

// TestCreateDriveUploadParentSubpastaValida cobre o caminho feliz do parâmetro
// `parent` (achado B1 da revisão): uma subpasta dentro da árvore de {id} é
// aceita, e a sessão resumable é aberta com `target` = a SUBPASTA, não a raiz
// {id} — mesmo padrão de handleUploadDriveFile (handlers_drive_folders.go) e
// do driveTreeServer que os testes de checkDriveFolderNesting já usam
// (drive_test.go/handlers_drive_folders_test.go) pra fakear a resposta de
// `parents` do Google.
func TestCreateDriveUploadParentSubpastaValida(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	var gotBody map[string]any
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet: // driveIsDescendantCached → IsDescendant consultando "parents"
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"parents": []string{"root123"}})
		case http.MethodPost: // StartResumableUploadAs
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Location", "https://upload.example/session/sub456")
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("método inesperado: %s", r.Method)
		}
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123", UploadAccount: driveAccountContratos}

	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/drive-folders/"+validUUID+"/uploads?parent=sub456",
		strings.NewReader(`{"name":"aula.mp4","mimeType":"video/mp4","size":1000}`)), 1)
	s.createDriveUpload(w, r, folder)
	if w.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	parents, _ := gotBody["parents"].([]any)
	if len(parents) != 1 || parents[0] != "sub456" {
		t.Fatalf("StartResumableUploadAs recebeu parents=%v, esperava a subpasta [\"sub456\"] (não a raiz)", gotBody["parents"])
	}

	sess, err := s.getDriveUploadSession(context.Background(), mustExtractUploadID(t, w))
	if err != nil || sess == nil {
		t.Fatalf("sessão não foi salva: sess=%v err=%v", sess, err)
	}
	if sess.SessionURI != "https://upload.example/session/sub456" {
		t.Fatalf("SessionURI salvo = %q", sess.SessionURI)
	}
}

// mustExtractUploadID lê o uploadId do corpo de uma resposta 201 de
// createDriveUpload — atalho pros testes que precisam consultar a sessão
// salva depois de conferir a resposta HTTP.
func mustExtractUploadID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		UploadID string `json:"uploadId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	return out.UploadID
}

// TestCreateDriveUploadParentForaDaArvore cobre o 403 do achado B1: `parent`
// que não é descendente de {id} (driveIsDescendantCached devolvendo false) —
// o handler nunca chega a abrir sessão nenhuma no Drive (o fake falha o teste
// se receber um POST).
func TestCreateDriveUploadParentForaDaArvore(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet: // driveIsDescendantCached → nenhum parent bate com root123
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"parents": []string{}})
		case http.MethodPost:
			t.Fatal("não deveria abrir sessão de upload resumable com parent fora do escopo autorizado")
		default:
			t.Fatalf("método inesperado: %s", r.Method)
		}
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123", UploadAccount: driveAccountContratos}

	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/drive-folders/"+validUUID+"/uploads?parent=fora-da-arvore",
		strings.NewReader(`{"name":"aula.mp4","mimeType":"video/mp4","size":1000}`)), 1)
	s.createDriveUpload(w, r, folder)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != "FORBIDDEN" {
		t.Fatalf("code no corpo = %q", body["code"])
	}
}

// TestCreateDriveUploadRedisFalhaAposSucessoNoDrive cobre o achado B2: o
// Drive já abriu a sessão resumable (StartResumableUploadAs com sucesso) mas
// o Set no Redis falha logo em seguida — a sessão fica órfã no Drive (só
// expira sozinha em 1 semana, a API não oferece como abandoná-la mais cedo).
// O comportamento observável pro CLIENTE não pode mudar (continua 502
// UPLOAD_FAILED, nunca um 201 com uploadId que não existe em lugar nenhum) —
// é só isso que dá pra verificar aqui: o pacote não tem hoje nenhum jeito de
// capturar saída do slog em teste (nenhum outro teste do repo faz isso), e
// criar um do zero só pra este caso ficaria fora do escopo do achado. A
// checagem do log (folder id + nome do arquivo + sessionUri) é só por
// inspeção do código em handlers_drive_uploads.go.
func TestCreateDriveUploadRedisFalhaAposSucessoNoDrive(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = s.rdb.Close() }()

	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://upload.example/session/orfa")
		w.WriteHeader(http.StatusOK)
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123", UploadAccount: driveAccountContratos}

	// Redis "fora do ar" só DEPOIS que o Drive já respondeu com sucesso —
	// StartResumableUploadAs (acima) já rodou como sempre roda antes do Set;
	// fechar o miniredis aqui garante que é exatamente esse Set que falha.
	mr.Close()

	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/drive-folders/"+validUUID+"/uploads",
		strings.NewReader(`{"name":"aula.mp4","mimeType":"video/mp4","size":1000}`)), 1)
	s.createDriveUpload(w, r, folder)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s — comportamento pro cliente não pode mudar com a falha do Redis", w.Code, w.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != "UPLOAD_FAILED" {
		t.Fatalf("code no corpo = %q", body["code"])
	}
}

// ── putDriveUploadChunk (miolo) ──────────────────────────────────────────

// startSession grava uma sessão válida no Redis do Server de teste e devolve
// o uploadId — atalho comum aos testes de PUT/GET que precisam de uma sessão
// já existente sem passar pelo handler de criação.
func startSession(t *testing.T, s *Server, sess driveUploadSession) string {
	t.Helper()
	uploadID := randomToken(32)
	if err := s.saveDriveUploadSession(context.Background(), uploadID, sess); err != nil {
		t.Fatalf("saveDriveUploadSession: %v", err)
	}
	return uploadID
}

func chunkReq(uploadID, contentRange, body string, userID int64) *http.Request {
	r := httptest.NewRequest("PUT", "/drive-folders/"+validUUID+"/uploads/"+uploadID, strings.NewReader(body))
	r.SetPathValue("id", validUUID)
	r.SetPathValue("uploadId", uploadID)
	if contentRange != "" {
		r.Header.Set("Content-Range", contentRange)
	}
	r.ContentLength = int64(len(body))
	return reqAs(r, userID)
}

func TestPutDriveUploadChunkUploadIdDeOutroUsuario(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}
	uploadID := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: validUUID, Size: 100, MimeType: "application/octet-stream", UserID: 1,
	})

	// mesmo uploadId, usuário DIFERENTE do dono → 404 (não 403 — não revela
	// que o uploadId existe e pertence a outra pessoa).
	w := httptest.NewRecorder()
	s.putDriveUploadChunk(w, chunkReq(uploadID, "bytes 0-9/100", "0123456789", 2), folder)
	if w.Code != http.StatusNotFound {
		t.Fatalf("dono diferente: code=%d", w.Code)
	}

	// uploadId que nunca existiu → mesmo 404.
	w2 := httptest.NewRecorder()
	s.putDriveUploadChunk(w2, chunkReq(randomToken(32), "bytes 0-9/100", "0123456789", 1), folder)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("uploadId inexistente: code=%d", w2.Code)
	}

	// uploadId mal formado (não é hex de 64 chars) → mesmo 404.
	w3 := httptest.NewRecorder()
	s.putDriveUploadChunk(w3, chunkReq("nao-e-um-upload-id", "bytes 0-9/100", "0123456789", 1), folder)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("uploadId mal formado: code=%d", w3.Code)
	}

	// sessão de uma pasta DIFERENTE (mesmo dono) → mesmo 404.
	uploadIDOutraPasta := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: "outra-pasta-uuid", Size: 100, MimeType: "application/octet-stream", UserID: 1,
	})
	w4 := httptest.NewRecorder()
	s.putDriveUploadChunk(w4, chunkReq(uploadIDOutraPasta, "bytes 0-9/100", "0123456789", 1), folder)
	if w4.Code != http.StatusNotFound {
		t.Fatalf("sessão de outra pasta: code=%d", w4.Code)
	}
}

func TestPutDriveUploadChunkContentRangeInvalido(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("não deveria chegar a chamar o Drive com Content-Range inválido")
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}
	uploadID := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: validUUID, Size: 100, MimeType: "application/octet-stream", UserID: 1,
	})

	cases := []string{"", "garbage", "bytes 10-5/100", "bytes 0-99/50"}
	for _, cr := range cases {
		w := httptest.NewRecorder()
		s.putDriveUploadChunk(w, chunkReq(uploadID, cr, "0123456789", 1), folder)
		if w.Code != http.StatusBadRequest {
			t.Errorf("Content-Range=%q: code=%d, esperava 400", cr, w.Code)
		}
	}
}

func TestPutDriveUploadChunkTotalDivergente(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}
	// sessão diz 100 bytes no total; o pedaço afirma 200 — recusa antes de
	// chamar o Drive.
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("não deveria chegar a chamar o Drive com total divergente")
	})
	uploadID := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: validUUID, Size: 100, MimeType: "application/octet-stream", UserID: 1,
	})
	w := httptest.NewRecorder()
	s.putDriveUploadChunk(w, chunkReq(uploadID, "bytes 0-9/200", "0123456789", 1), folder)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", w.Code)
	}
}

// TestPutDriveUploadChunkContentTypeMentiroso cobre o sniff do primeiro
// pedaço: sessão declarou "video/mp4", mas os bytes reais são texto puro —
// 400, e a sessão é apagada do Redis (o front tem que recomeçar declarando
// o tipo certo, não adianta só tentar de novo com o mesmo uploadId).
func TestPutDriveUploadChunkContentTypeMentiroso(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("não deveria chegar a mandar o pedaço pro Drive")
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}
	uploadID := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: validUUID, Size: 1000, MimeType: "video/mp4", UserID: 1,
	})

	texto := strings.Repeat("isto e texto puro, nao e video\n", 20) // >512 bytes de HTML/texto simples
	w := httptest.NewRecorder()
	r := chunkReq(uploadID, "bytes 0-"+strconv.Itoa(len(texto)-1)+"/1000", texto, 1)
	s.putDriveUploadChunk(w, r, folder)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}

	// sessão apagada: uma nova tentativa com o MESMO uploadId dá 404 (não
	// existe mais), não repete o mesmo 400 pra sempre.
	w2 := httptest.NewRecorder()
	s.putDriveUploadChunk(w2, chunkReq(uploadID, "bytes 0-9/1000", "0123456789", 1), folder)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("sessão deveria ter sido apagada após Content-Type mentiroso: code=%d", w2.Code)
	}
}

// TestPutDriveUploadChunkPrimeiroPedacoValido: mesmo cenário acima, mas com
// bytes reais de vídeo (assinatura MP4 mínima) — passa no sniff e chega a
// chamar o Drive.
func TestPutDriveUploadChunkPrimeiroPedacoValido(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	var driveChamado bool
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		driveChamado = true
		w.Header().Set("Range", "bytes=0-9")
		w.WriteHeader(308)
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}
	uploadID := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: validUUID, Size: 1000, MimeType: "video/mp4", UserID: 1,
	})

	// assinatura de um MP4 "ftyp" — http.DetectContentType reconhece como
	// vídeo (video/mp4), que está na allowlist "segura pra inline".
	mp4Signature := "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"
	w := httptest.NewRecorder()
	r := chunkReq(uploadID, "bytes 0-"+strconv.Itoa(len(mp4Signature)-1)+"/1000", mp4Signature, 1)
	s.putDriveUploadChunk(w, r, folder)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if !driveChamado {
		t.Fatal("esperava que o pedaço válido chegasse a chamar o Drive")
	}
}

// TestPutDriveUploadChunkSessaoPerdidaApagaRegistro cobre o 410 do handler:
// o Drive diz que a sessão sumiu, o handler responde UPLOAD_SESSION_LOST e
// apaga o registro (uma nova tentativa com o mesmo uploadId dá 404).
func TestPutDriveUploadChunkSessaoPerdidaApagaRegistro(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}
	uploadID := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: validUUID, Size: 100, MimeType: "application/octet-stream", UserID: 1,
	})

	w := httptest.NewRecorder()
	s.putDriveUploadChunk(w, chunkReq(uploadID, "bytes 0-9/100", "0123456789", 1), folder)
	if w.Code != http.StatusGone {
		t.Fatalf("code=%d", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != "UPLOAD_SESSION_LOST" {
		t.Fatalf("code no corpo = %q", body["code"])
	}

	w2 := httptest.NewRecorder()
	s.putDriveUploadChunk(w2, chunkReq(uploadID, "bytes 0-9/100", "0123456789", 1), folder)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("registro deveria ter sido apagado: code=%d", w2.Code)
	}
}

// ── getDriveUploadStatus (miolo) ─────────────────────────────────────────

func statusReq(uploadID string, userID int64) *http.Request {
	r := httptest.NewRequest("GET", "/drive-folders/"+validUUID+"/uploads/"+uploadID, nil)
	r.SetPathValue("id", validUUID)
	r.SetPathValue("uploadId", uploadID)
	return reqAs(r, userID)
}

func TestGetDriveUploadStatusUploadIdDeOutroUsuario(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}
	uploadID := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: validUUID, Size: 100, MimeType: "application/octet-stream", UserID: 1,
	})
	w := httptest.NewRecorder()
	s.getDriveUploadStatus(w, statusReq(uploadID, 2), folder)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestGetDriveUploadStatusEmAndamento(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Range"); got != "bytes */100" {
			t.Fatalf("Content-Range = %q", got)
		}
		w.Header().Set("Range", "bytes=0-49")
		w.WriteHeader(308)
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}
	uploadID := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: validUUID, Size: 100, MimeType: "application/octet-stream", UserID: 1,
	})
	w := httptest.NewRecorder()
	s.getDriveUploadStatus(w, statusReq(uploadID, 1), folder)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Done     bool  `json:"done"`
		Received int64 `json:"received"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Done || out.Received != 50 {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetDriveUploadStatusConcluidoApagaRegistro(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	s.drive = fakeDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "f1", "name": "aula.mp4", "mimeType": "video/mp4"})
	})
	folder := &DriveFolder{ID: validUUID, DriveFolderID: "root123"}
	uploadID := startSession(t, s, driveUploadSession{
		SessionURI: "https://upload.example/s", FolderID: validUUID, Size: 100, MimeType: "video/mp4", UserID: 1,
	})
	w := httptest.NewRecorder()
	s.getDriveUploadStatus(w, statusReq(uploadID, 1), folder)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	sess, err := s.getDriveUploadSession(context.Background(), uploadID)
	if err != nil {
		t.Fatalf("getDriveUploadSession: %v", err)
	}
	if sess != nil {
		t.Fatalf("sessão deveria ter sido apagada após done=true, veio %+v", sess)
	}
}
