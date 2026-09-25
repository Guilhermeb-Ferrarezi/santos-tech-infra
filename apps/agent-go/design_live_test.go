package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPartialWriteInput(t *testing.T) {
	full := `{"file_path":"/w/telas/a.html","content":"<h1>Oi\nvocê</h1>"}`
	fp, c, ok := partialWriteInput(full)
	if !ok || fp != "/w/telas/a.html" || c != "<h1>Oi\nvocê</h1>" {
		t.Fatalf("completo: %q %q %v", fp, c, ok)
	}
	// cortado no meio do content, inclusive no meio de um escape
	for _, cut := range []string{
		`{"file_path":"/w/telas/a.html","content":"<h1>Oi`,
		`{"file_path":"/w/telas/a.html","content":"<h1>Oi\`,
		`{"file_path":"/w/telas/a.html","content":"<h1>Oi\u00`,
	} {
		fp, c, ok := partialWriteInput(cut)
		if !ok || fp != "/w/telas/a.html" || !strings.HasPrefix(c, "<h1>Oi") {
			t.Fatalf("parcial %q: %q %q %v", cut, fp, c, ok)
		}
	}
	// content ainda não começou
	if _, _, ok := partialWriteInput(`{"file_path":"/w/telas/a.ht`); ok {
		t.Fatal("sem content não deveria extrair nada")
	}
}

// liveHarness grava os eventos emitidos e controla o relógio do throttle.
func liveHarness(t *testing.T) (*designLive, *[]turnEvent, *time.Time) {
	t.Helper()
	evs := &[]turnEvent{}
	now := time.Unix(1000, 0)
	d := newDesignLive("/data/w/p1", func(ev turnEvent) { *evs = append(*evs, ev) })
	d.now = func() time.Time { return now }
	return d, evs, &now
}

func streamEv(e map[string]any) map[string]any {
	return map[string]any{"type": "stream_event", "event": e}
}

func deltaEv(idx int, pj string) map[string]any {
	return streamEv(map[string]any{"type": "content_block_delta", "index": float64(idx),
		"delta": map[string]any{"type": "input_json_delta", "partial_json": pj}})
}

func TestDesignLiveRascunhoDoWriteComThrottle(t *testing.T) {
	d, evs, now := liveHarness(t)
	d.observe(streamEv(map[string]any{"type": "content_block_start", "index": float64(1),
		"content_block": map[string]any{"type": "tool_use", "name": "Write", "id": "tu1"}}))
	d.observe(deltaEv(1, `{"file_path":"/data/w/p1/telas/login.html","content":"<html><body>`))
	if len(*evs) != 1 || (*evs)[0].Type != "design_draft" {
		t.Fatalf("1º pedaço com conteúdo deveria virar rascunho: %+v", *evs)
	}
	data := (*evs)[0].Data.(map[string]any)
	if data["path"] != "telas/login.html" || data["html"] != "<html><body>" || data["done"] != false {
		t.Fatalf("rascunho = %+v", data)
	}
	d.observe(deltaEv(1, `<h1>Entrar`)) // dentro do throttle: segura
	if len(*evs) != 1 {
		t.Fatalf("throttle falhou: %d eventos", len(*evs))
	}
	*now = now.Add(draftThrottle)
	d.observe(deltaEv(1, `</h1>`))
	if len(*evs) != 2 || (*evs)[1].Data.(map[string]any)["html"] != "<html><body><h1>Entrar</h1>" {
		t.Fatalf("depois do throttle: %+v", *evs)
	}
	d.observe(deltaEv(1, `</body></html>"}`))
	d.observe(streamEv(map[string]any{"type": "content_block_stop", "index": float64(1)}))
	last := (*evs)[len(*evs)-1].Data.(map[string]any)
	if last["done"] != true || last["html"] != "<html><body><h1>Entrar</h1></body></html>" {
		t.Fatalf("fim do bloco deveria mandar o rascunho completo: %+v", last)
	}
}

func TestDesignLiveIgnoraForaDeTelas(t *testing.T) {
	d, evs, _ := liveHarness(t)
	d.observe(streamEv(map[string]any{"type": "content_block_start", "index": float64(0),
		"content_block": map[string]any{"type": "tool_use", "name": "Write"}}))
	d.observe(deltaEv(0, `{"file_path":"/data/w/p1/CLAUDE.md","content":"# guia`))
	d.observe(deltaEv(0, `"}`))
	d.observe(streamEv(map[string]any{"type": "content_block_stop", "index": float64(0)}))
	d.observe(streamEv(map[string]any{"type": "content_block_start", "index": float64(1),
		"content_block": map[string]any{"type": "tool_use", "name": "Write"}}))
	d.observe(deltaEv(1, `{"file_path":"/etc/passwd","content":"x"}`))
	d.observe(streamEv(map[string]any{"type": "content_block_stop", "index": float64(1)}))
	if len(*evs) != 0 {
		t.Fatalf("nada fora de telas/ vira rascunho: %+v", *evs)
	}
}

func TestDesignLiveArquivoSalvoViraDesignFile(t *testing.T) {
	d, evs, _ := liveHarness(t)
	d.observe(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "e1", "name": "Edit", "input": map[string]any{"file_path": "/data/w/p1/telas/index.html"}},
		map[string]any{"type": "tool_use", "id": "r1", "name": "Read", "input": map[string]any{"file_path": "/data/w/p1/telas/index.html"}},
		map[string]any{"type": "tool_use", "id": "e2", "name": "Edit", "input": map[string]any{"file_path": "/data/w/p1/telas/x.html"}},
	}}})
	d.observe(map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "r1", "content": "..."},
		map[string]any{"type": "tool_result", "tool_use_id": "e1", "content": "ok"},
		map[string]any{"type": "tool_result", "tool_use_id": "e2", "content": "erro", "is_error": true},
	}}})
	if len(*evs) != 1 || (*evs)[0].Type != "design_file" || (*evs)[0].Data.(map[string]any)["path"] != "telas/index.html" {
		t.Fatalf("só o Edit bem-sucedido vira design_file: %+v", *evs)
	}
}

func serveCanvas(t *testing.T, workdir, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{cfg: Config{JWTSecret: "segredo-de-teste", CORSOrigins: []string{"https://santos-tech.com"}}}
	s.designWorkdirFor = func(_ *http.Request, id string) (string, error) { return workdir, nil }
	req := httptest.NewRequest(http.MethodGet, "/claude/designs/conv-1/c/"+token+"/"+path, nil)
	req.SetPathValue("id", "conv-1")
	req.SetPathValue("token", token)
	req.SetPathValue("path", path)
	rec := httptest.NewRecorder()
	s.handleDesignCanvas(rec, req)
	return rec
}

func TestCanvasServeCascaEAssetsComToken(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "assets", "logo.svg"), []byte("<svg/>"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("guia"), 0o644)
	tok := previewToken("segredo-de-teste", "conv-1", previewTokenTTL)

	rec := serveCanvas(t, dir, tok, "")
	csp := rec.Header().Get("Content-Security-Policy")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "santos-design-canvas") {
		t.Fatalf("casca: %d", rec.Code)
	}
	for _, precisa := range []string{"sandbox allow-scripts allow-forms", "base-uri 'self'", "connect-src 'none'", "frame-ancestors https://santos-tech.com"} {
		if !strings.Contains(csp, precisa) {
			t.Fatalf("CSP da casca sem %q: %s", precisa, csp)
		}
	}
	if rec := serveCanvas(t, dir, tok, "assets/logo.svg"); rec.Code != 200 {
		t.Fatalf("asset: %d", rec.Code)
	}
	for _, p := range []string{"CLAUDE.md", "telas/../CLAUDE.md", "design.json"} {
		if rec := serveCanvas(t, dir, tok, p); rec.Code != 404 {
			t.Fatalf("%q deveria ser 404, veio %d", p, rec.Code)
		}
	}
	if rec := serveCanvas(t, dir, "1.invalido", ""); rec.Code != 404 {
		t.Fatalf("token inválido: %d", rec.Code)
	}
}

func TestCanvasShellEmbutido(t *testing.T) {
	// a casca embutida precisa falar o protocolo que o painel espera
	for _, precisa := range []string{`type: "ready"`, `"render"`, `"navigate"`, "santos-design-inspect", "srcdoc"} {
		if !strings.Contains(string(canvasShellHTML), precisa) {
			t.Fatalf("canvas_shell.html sem %q", precisa)
		}
	}
}
