package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// generateRequest é o corpo de POST /claude/generate.
type generateRequest struct {
	Task     string `json:"task"`     // "email" | "rewrite" | "subjects" (default: email)
	Brief    string `json:"brief"`    // descrição do que gerar / instrução de reescrita
	Tone     string `json:"tone"`     // tom desejado (opcional)
	Audience string `json:"audience"` // público-alvo (opcional)
	HTML     string `json:"html"`     // conteúdo atual (obrigatório em "rewrite")
}

// generateResult é o que devolvemos ao chamador (o painel de email).
type generateResult struct {
	Subject  string   `json:"subject,omitempty"`
	HTML     string   `json:"html,omitempty"`
	Text     string   `json:"text,omitempty"`
	Subjects []string `json:"subjects,omitempty"`
}

// handleGenerate gera conteúdo de email/template num único turno (stateless): não
// cria conversa, não persiste nada. Roda o Claude com ambiente mínimo (só OAuth).
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	req.Task = strings.TrimSpace(req.Task)
	if req.Task == "" {
		req.Task = "email"
	}
	switch req.Task {
	case "rewrite":
		if strings.TrimSpace(req.HTML) == "" {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "informe o html a reescrever"))
			return
		}
	case "email", "subjects":
		if strings.TrimSpace(req.Brief) == "" {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "informe um brief"))
			return
		}
	default:
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "task inválida (use email, rewrite ou subjects)"))
		return
	}

	raw, err := s.generateOnce(r.Context(), buildGeneratePrompt(req))
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := parseGenerateResult(raw)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "GENERATION_FAILED", "não consegui interpretar a resposta da IA"))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// generateOnce roda `claude -p --output-format json` num turno isolado e devolve o
// texto final (campo .result). Sem MCP, sem --add-dir, sem skip-permissions e sem
// tokens de infra no ambiente — é redação de texto, não automação.
func (s *Server) generateOnce(ctx context.Context, prompt string) (string, error) {
	dir, err := os.MkdirTemp(s.cfg.WorkspaceRoot, "gen-*")
	if err != nil {
		if dir, err = os.MkdirTemp("", "gen-*"); err != nil {
			return "", err
		}
	}
	defer os.RemoveAll(dir)

	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, s.cfg.ClaudeBin,
		"-p", "--output-format", "json", "--model", s.cfg.DefaultModel)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)

	// Ambiente mínimo: só o token OAuth da assinatura.
	env := os.Environ()
	if tok, terr := s.oauthToken(ctx); terr == nil && tok != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+tok)
	}
	cmd.Env = env

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr // logs do CLI vão pro stderr do serviço
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude falhou: %w", err)
	}

	// --output-format json: um envelope com o texto final em .result.
	var env2 struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env2); err != nil {
		return "", fmt.Errorf("resposta do claude inválida: %w", err)
	}
	if env2.IsError || strings.TrimSpace(env2.Result) == "" {
		return "", fmt.Errorf("claude não retornou resultado (subtype=%s)", env2.Subtype)
	}
	return env2.Result, nil
}

// buildGeneratePrompt monta a instrução conforme a tarefa. Em todos os casos o
// modelo é instruído a responder EXCLUSIVAMENTE com JSON (sem cercas, sem ferramentas).
func buildGeneratePrompt(req generateRequest) string {
	tone := strings.TrimSpace(req.Tone)
	if tone == "" {
		tone = "profissional, claro e acolhedor"
	}
	audience := strings.TrimSpace(req.Audience)
	if audience == "" {
		audience = "leads da Santos Tech"
	}

	var b strings.Builder
	b.WriteString("Você é um redator de email marketing da Santos Tech.\n")
	b.WriteString("Responda em português do Brasil. NÃO use ferramentas, NÃO acesse arquivos e NÃO rode comandos — apenas escreva.\n\n")

	switch req.Task {
	case "subjects":
		fmt.Fprintf(&b, "Gere 5 opções de linha de assunto para o email descrito, otimizadas para taxa de abertura.\nBriefing: %s\nTom: %s\nPúblico: %s\n\n", req.Brief, tone, audience)
		b.WriteString(`Responda EXCLUSIVAMENTE com um JSON válido, sem cercas de código, no formato:`)
		b.WriteString("\n{\"subjects\": [\"...\", \"...\", \"...\", \"...\", \"...\"]}\n")
	case "rewrite":
		fmt.Fprintf(&b, "Reescreva/ajuste o email HTML abaixo conforme a instrução, preservando as merge tags {{name}} e {{email}}.\nInstrução: %s\nTom desejado: %s\n\nHTML atual:\n%s\n\n", req.Brief, tone, req.HTML)
		writeEmailFormatRules(&b)
	default: // "email"
		fmt.Fprintf(&b, "Crie um email pronto.\nBriefing: %s\nTom: %s\nPúblico: %s\n\n", req.Brief, tone, audience)
		writeEmailFormatRules(&b)
	}
	return b.String()
}

func writeEmailFormatRules(b *strings.Builder) {
	b.WriteString("Regras do corpo:\n")
	b.WriteString("- Use as merge tags {{name}} e {{email}} quando fizer sentido personalizar.\n")
	b.WriteString("- O HTML deve ser inline e responsivo, apenas o conteúdo (sem <html>, <head> ou <body>).\n\n")
	b.WriteString(`Responda EXCLUSIVAMENTE com um JSON válido, sem cercas de código, no formato:`)
	b.WriteString("\n{\"subject\": \"...\", \"html\": \"...\", \"text\": \"...\"}\n")
	b.WriteString("onde \"text\" é a versão em texto puro do email.\n")
}

// parseGenerateResult extrai o objeto JSON da resposta do modelo, tolerando cercas
// de código e texto ao redor (pega do primeiro "{" ao último "}").
func parseGenerateResult(raw string) (*generateResult, error) {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j >= i {
			s = s[i : j+1]
		}
	}
	var res generateResult
	if err := json.Unmarshal([]byte(s), &res); err != nil {
		return nil, err
	}
	if res.Subject == "" && res.HTML == "" && res.Text == "" && len(res.Subjects) == 0 {
		return nil, fmt.Errorf("resposta vazia")
	}
	return &res, nil
}
