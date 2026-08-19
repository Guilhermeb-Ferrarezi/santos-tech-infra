package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// handleGenerateStream é a variante streaming de /claude/generate: emite Server-Sent
// Events com o texto sendo escrito ao vivo (event: delta) e, ao final, o JSON já
// interpretado (event: done) ou um erro (event: error). É aditivo — o /claude/generate
// one-shot continua existindo para quem não quer stream.
func (s *Server) handleGenerateStream(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := decodeJSONLimit(r, &req, maxGenerateBody); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if err := normalizeGenerate(&req); err != nil {
		writeErr(w, err)
		return
	}

	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, appErr(http.StatusInternalServerError, "STREAM_UNSUPPORTED", "streaming indisponível"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // pede pra proxies não bufferizarem
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	if err := s.generateStream(r.Context(), req.Task, buildGeneratePrompt(req, false), w, fl); err != nil {
		sseEvent(w, fl, "error", map[string]string{"message": err.Error()})
	}
}

// generateStream roda o claude com stream-json e repassa os deltas de texto, depois
// emite o resultado final já parseado. Mesmo ambiente mínimo do generateOnce.
func (s *Server) generateStream(ctx context.Context, task, prompt string, w http.ResponseWriter, fl http.Flusher) error {
	dir, err := os.MkdirTemp(s.cfg.WorkspaceRoot, "gen-*")
	if err != nil {
		if dir, err = os.MkdirTemp("", "gen-*"); err != nil {
			return err
		}
	}
	defer os.RemoveAll(dir)

	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, s.cfg.ClaudeBin,
		"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--model", s.cfg.DefaultModel)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)

	// Mesmo ambiente mínimo do generateOnce (allow-list explícita, sem segredos
	// de infra). Ver claudeEnv em session.go.
	cmd.Env = s.claudeEnv(ctx, nil)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("claude falhou ao iniciar: %w", err)
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	var acc strings.Builder // texto acumulado (fallback de parse)
	var finalText string    // texto autoritativo do evento "result"
	var usage usageFields
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev["type"] {
		case "stream_event":
			if t := deltaText(ev); t != "" {
				acc.WriteString(t)
				sseEvent(w, fl, "delta", map[string]string{"text": t})
			}
		case "result":
			usage = usageFromMap(ev)
			if rs, _ := ev["result"].(string); rs != "" {
				finalText = rs
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("claude saiu com erro: %w", err)
	}
	if usage != (usageFields{}) {
		s.recordUsage(ctx, "generate_stream", task, s.cfg.DefaultModel, "", usage)
	}
	if finalText == "" {
		finalText = acc.String()
	}
	res, err := parseGenerateResult(finalText)
	if err != nil {
		return fmt.Errorf("não consegui interpretar a resposta da IA")
	}
	sseEvent(w, fl, "done", res)
	return nil
}

// normalizeGenerate aplica os defaults e valida a generateRequest (compartilhado entre
// o handler one-shot e o streaming).
func normalizeGenerate(req *generateRequest) error {
	req.Task = strings.TrimSpace(req.Task)
	if req.Task == "" {
		req.Task = "email"
	}
	switch req.Task {
	case "rewrite", "spamcheck":
		if strings.TrimSpace(req.HTML) == "" {
			return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "informe o html")
		}
	case "email", "subjects", "insights", "command", "diagram":
		if strings.TrimSpace(req.Brief) == "" {
			return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "informe um brief")
		}
	case "raw":
		if strings.TrimSpace(req.Brief) == "" {
			return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "brief obrigatório para task raw")
		}
	default:
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "task inválida (use email, rewrite, subjects, spamcheck, insights, command ou diagram)")
	}
	switch req.Model {
	case "", "sonnet", "opus", "haiku":
	default:
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "model inválido (use sonnet, opus ou haiku)")
	}
	switch req.Kind {
	case "", "flowchart", "sequence", "class":
	default:
		return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "kind inválido (use flowchart, sequence ou class)")
	}
	for _, t := range req.Tools {
		if _, ok := diagramTools[t]; !ok {
			return appErr(http.StatusBadRequest, "VALIDATION_ERROR", "tool inválida (use santos, notion ou miro)")
		}
	}
	return nil
}

// sseEvent serializa data em JSON e escreve um frame SSE, dando flush em seguida.
func sseEvent(w http.ResponseWriter, fl http.Flusher, event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	fl.Flush()
}
