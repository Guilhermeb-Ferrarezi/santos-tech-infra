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
	Task     string `json:"task"`     // "email" | "rewrite" | "subjects" | "spamcheck" | "command" (default: email)
	Brief    string `json:"brief"`    // descrição do que gerar / instrução de reescrita / pedido do usuário (command)
	Tone     string `json:"tone"`     // tom desejado (opcional)
	Audience string `json:"audience"` // público-alvo (opcional)
	Subject  string `json:"subject"`  // assunto a analisar (opcional em "spamcheck")
	HTML     string `json:"html"`     // conteúdo atual (obrigatório em "rewrite"/"spamcheck")
	Context  string `json:"context"`  // catálogo de ações + dados (task "command"); montado pelo painel
}

// spamIssue é um problema de entregabilidade apontado pela análise de spam.
type spamIssue struct {
	Level string `json:"level"` // "alto" | "medio" | "baixo"
	Text  string `json:"text"`
}

// generateResult é o que devolvemos ao chamador (o painel de email).
type generateResult struct {
	Subject   string      `json:"subject,omitempty"`
	Preheader string      `json:"preheader,omitempty"` // texto de pré-visualização (já embutido oculto no HTML)
	HTML      string      `json:"html,omitempty"`
	Text      string      `json:"text,omitempty"`
	Subjects  []string    `json:"subjects,omitempty"`
	Score     *int        `json:"score,omitempty"`   // nota 0-100 (spamcheck)
	Summary   string      `json:"summary,omitempty"` // resumo da análise (spamcheck)
	Issues    []spamIssue `json:"issues,omitempty"`  // problemas encontrados (spamcheck)
	// task "command": ação escolhida pelo LLM (o painel valida e executa), seus
	// argumentos (strings) e a resposta curta em PT-BR para o usuário.
	Action string            `json:"action,omitempty"`
	Args   map[string]string `json:"args,omitempty"`
	Reply  string            `json:"reply,omitempty"`
}

// handleGenerate gera conteúdo de email/template num único turno (stateless): não
// cria conversa, não persiste nada. Roda o Claude com ambiente mínimo (só OAuth).
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if err := normalizeGenerate(&req); err != nil {
		writeErr(w, err)
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

	// A task "command" tem um sistema de prompt próprio (assistente do painel, não
	// redator), então é tratada antes do cabeçalho de redação.
	if req.Task == "command" {
		var c strings.Builder
		c.WriteString("Você é o assistente do painel de email da Santos Tech. Responda em português do Brasil.\n")
		c.WriteString("NÃO use ferramentas, NÃO acesse arquivos e NÃO rode comandos. Sua única saída é um JSON.\n\n")
		fmt.Fprintf(&c, "Pedido do usuário:\n%s\n\n", strings.TrimSpace(req.Brief))
		fmt.Fprintf(&c, "%s\n\n", strings.TrimSpace(req.Context))
		c.WriteString("Escolha UMA ação do catálogo acima que melhor atende ao pedido. ")
		c.WriteString("Se nenhuma ação servir (ex.: é só uma pergunta), use action \"answer\" e responda no campo reply.\n")
		c.WriteString("Regras:\n")
		c.WriteString("- \"action\" deve ser exatamente um id de ação do catálogo (ou \"answer\").\n")
		c.WriteString("- \"args\" contém apenas os parâmetros daquela ação, todos como string. Use {} se não houver.\n")
		c.WriteString("- \"reply\": 1 a 2 frases em PT-BR dizendo o que você fez ou respondendo a pergunta.\n\n")
		c.WriteString("Responda EXCLUSIVAMENTE com um JSON válido, sem cercas de código, no formato:\n")
		c.WriteString("{\"action\": \"...\", \"args\": {\"...\": \"...\"}, \"reply\": \"...\"}\n")
		return c.String()
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
	case "spamcheck":
		subject := strings.TrimSpace(req.Subject)
		if subject == "" {
			subject = "(sem assunto informado)"
		}
		fmt.Fprintf(&b, "Avalie o email abaixo quanto à ENTREGABILIDADE e risco de cair em spam.\nAssunto: %s\n\nHTML:\n%s\n\n", subject, req.HTML)
		b.WriteString("Considere: palavras-gatilho de spam, excesso de MAIÚSCULAS e pontos de exclamação, proporção alta de imagens vs texto, muitos links ou links suspeitos, ausência de versão em texto, ausência de descadastro, imagens sem alt, assunto enganoso.\n")
		b.WriteString("Dê uma nota de 0 a 100 (100 = ótima entregabilidade, risco baixo), um resumo curto e a lista de problemas, cada um com nível \"alto\", \"medio\" ou \"baixo\".\n\n")
		b.WriteString(`Responda EXCLUSIVAMENTE com um JSON válido, sem cercas de código, no formato:`)
		b.WriteString("\n{\"score\": 0, \"summary\": \"...\", \"issues\": [{\"level\": \"alto\", \"text\": \"...\"}]}\n")
	case "insights":
		fmt.Fprintf(&b, "Você recebeu as métricas de email marketing abaixo. Dê recomendações práticas e priorizadas para melhorar taxa de abertura, cliques e entregabilidade.\n\nMétricas:\n%s\n\n", req.Brief)
		b.WriteString("Seja específico e acionável (ex: testar assuntos mais curtos, segmentar inativos, ajustar horário de envio, reduzir links). Use \"level\" como prioridade: \"alto\" = maior impacto.\n\n")
		b.WriteString(`Responda EXCLUSIVAMENTE com um JSON válido, sem cercas de código, no formato:`)
		b.WriteString("\n{\"summary\": \"...\", \"issues\": [{\"level\": \"alto\", \"text\": \"...\"}]}\n")
	default: // "email"
		fmt.Fprintf(&b, "Crie um email pronto.\nBriefing: %s\nTom: %s\nPúblico: %s\n\n", req.Brief, tone, audience)
		writeEmailFormatRules(&b)
	}
	return b.String()
}

func writeEmailFormatRules(b *strings.Builder) {
	b.WriteString("Regras do corpo:\n")
	b.WriteString("- Use as merge tags {{name}} e {{email}} quando fizer sentido personalizar.\n")
	b.WriteString("- O HTML deve ser inline e responsivo, apenas o conteúdo (sem <html>, <head> ou <body>).\n")
	b.WriteString("- Comece o HTML com um pré-cabeçalho OCULTO (preheader): um <div style=\"display:none;max-height:0;overflow:hidden;mso-hide:all;\"> com 40 a 90 caracteres que resumem o email — é o texto que aparece ao lado do assunto na caixa de entrada.\n\n")
	b.WriteString(`Responda EXCLUSIVAMENTE com um JSON válido, sem cercas de código, no formato:`)
	b.WriteString("\n{\"subject\": \"...\", \"preheader\": \"...\", \"html\": \"...\", \"text\": \"...\"}\n")
	b.WriteString("onde \"preheader\" é o mesmo texto do pré-cabeçalho oculto e \"text\" é a versão em texto puro do email.\n")
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
	if res.Subject == "" && res.HTML == "" && res.Text == "" && len(res.Subjects) == 0 &&
		res.Score == nil && res.Summary == "" && len(res.Issues) == 0 &&
		res.Action == "" && res.Reply == "" {
		return nil, fmt.Errorf("resposta vazia")
	}
	return &res, nil
}
