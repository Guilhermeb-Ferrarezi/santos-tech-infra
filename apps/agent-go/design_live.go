package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// Canvas ao vivo do Claude Design.
//
// Com --include-partial-messages o CLI emite o input de cada tool_use em pedaços
// (stream_event → content_block_delta → input_json_delta) ENQUANTO o modelo escreve.
// Pro Write de uma tela, isso é o HTML sendo digitado: o designLive junta os pedaços,
// extrai o `content` parcial e manda pro painel (`design_draft`), que injeta na casca
// do canvas — a tela vai se montando. Quando a ferramenta termina de verdade (o
// tool_result chega), `design_file` avisa que o arquivo em disco mudou.
//
// Verificado contra o CLI 2.1.282: um Write de 3,6 KB chegou em 382 deltas.

// draftThrottle: intervalo mínimo entre dois rascunhos da mesma tela. Cada rascunho é
// uma renderização no navegador; mais rápido que isso só gasta CPU.
const draftThrottle = 450 * time.Millisecond

// designEditTools: ferramentas que mexem em arquivo — o tool_result delas vira design_file.
var designEditTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true}

type draftBlock struct {
	name     string
	json     strings.Builder
	lastEmit time.Time
	lastLen  int
}

// designLive acompanha UMA sessão viva de design. Só é usado pelo readLoop (uma
// goroutine), então não precisa de lock.
type designLive struct {
	workdir string
	emit    func(turnEvent)
	now     func() time.Time
	blocks  map[int]*draftBlock // índice do content block → Write em andamento
	pending map[string]string   // tool_use_id → tela (telas/x.html) aguardando o resultado
}

func newDesignLive(workdir string, emit func(turnEvent)) *designLive {
	return &designLive{
		workdir: workdir, emit: emit, now: time.Now,
		blocks: map[int]*draftBlock{}, pending: map[string]string{},
	}
}

// relScreen converte o file_path absoluto do CLI em caminho relativo ao workdir, só
// pra arquivos do projeto em telas/ ou assets/ ("" pra qualquer outra coisa).
func (d *designLive) relScreen(p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(d.workdir, p)
	}
	rel, err := filepath.Rel(d.workdir, filepath.Clean(p))
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if clean, ok := canvasRel(rel); ok && clean == rel {
		return rel
	}
	return ""
}

// partialWriteInput extrai file_path e content de um JSON de input ainda incompleto.
// Tenta o JSON inteiro; senão fecha a string do content e o objeto, recuando alguns
// caracteres se o corte caiu no meio de um escape (\n, é...).
func partialWriteInput(js string) (filePath, content string, ok bool) {
	var in struct {
		FilePath string  `json:"file_path"`
		Content  *string `json:"content"`
	}
	try := func(s string) bool {
		in.FilePath, in.Content = "", nil
		return json.Unmarshal([]byte(s), &in) == nil && in.Content != nil
	}
	if try(js) {
		return in.FilePath, *in.Content, true
	}
	for cut := 0; cut <= 6 && cut <= len(js); cut++ {
		if try(js[:len(js)-cut] + `"}`) {
			return in.FilePath, *in.Content, true
		}
	}
	return "", "", false
}

// observe olha cada evento do stdout do CLI antes do handleEvent normal.
func (d *designLive) observe(ev map[string]any) {
	switch ev["type"] {
	case "stream_event":
		d.observeStream(ev)
	case "assistant":
		// Input completo do tool_use: guarda qual tela cada ferramenta vai mexer.
		for _, b := range messageContent(ev) {
			if b["type"] != "tool_use" {
				continue
			}
			name, _ := b["name"].(string)
			id, _ := b["id"].(string)
			input, _ := b["input"].(map[string]any)
			fp, _ := input["file_path"].(string)
			if rel := d.relScreen(fp); designEditTools[name] && id != "" && rel != "" {
				d.pending[id] = rel
			}
		}
	case "user":
		for _, b := range messageContent(ev) {
			if b["type"] != "tool_result" {
				continue
			}
			id, _ := b["tool_use_id"].(string)
			rel, ok := d.pending[id]
			if !ok {
				continue
			}
			delete(d.pending, id)
			if isErr, _ := b["is_error"].(bool); !isErr {
				d.emit(turnEvent{Type: "design_file", Data: map[string]any{"path": rel}})
			}
		}
	case "result":
		// Fim do turno: nada em andamento sobrevive pro próximo.
		d.blocks = map[int]*draftBlock{}
		d.pending = map[string]string{}
	}
}

func (d *designLive) observeStream(ev map[string]any) {
	e, _ := ev["event"].(map[string]any)
	idxF, _ := e["index"].(float64)
	idx := int(idxF)
	switch e["type"] {
	case "message_start":
		d.blocks = map[int]*draftBlock{}
	case "content_block_start":
		cb, _ := e["content_block"].(map[string]any)
		if cb["type"] == "tool_use" && cb["name"] == "Write" {
			d.blocks[idx] = &draftBlock{name: "Write"}
		}
	case "content_block_delta":
		b := d.blocks[idx]
		if b == nil {
			return
		}
		delta, _ := e["delta"].(map[string]any)
		if delta["type"] != "input_json_delta" {
			return
		}
		pj, _ := delta["partial_json"].(string)
		b.json.WriteString(pj)
		if d.now().Sub(b.lastEmit) >= draftThrottle {
			d.flush(b, false)
		}
	case "content_block_stop":
		if b := d.blocks[idx]; b != nil {
			d.flush(b, true)
			delete(d.blocks, idx)
		}
	}
}

// flush manda o rascunho atual da tela, se houver conteúdo novo.
func (d *designLive) flush(b *draftBlock, done bool) {
	fp, content, ok := partialWriteInput(b.json.String())
	if !ok || content == "" || (!done && len(content) == b.lastLen) {
		return
	}
	rel := d.relScreen(fp)
	if rel == "" || !strings.HasPrefix(rel, "telas/") || !strings.HasSuffix(rel, ".html") {
		return
	}
	b.lastEmit, b.lastLen = d.now(), len(content)
	d.emit(turnEvent{Type: "design_draft", Data: map[string]any{"path": rel, "html": content, "done": done}})
}
