package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxImageBytes = 8 << 20 // 8 MB

// imageExtFromMime valida o mime e devolve a extensão do arquivo.
func imageExtFromMime(mime string) (string, bool) {
	switch mime {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/webp":
		return "webp", true
	case "image/gif":
		return "gif", true
	}
	return "", false
}

// generateRequest é o corpo de POST /claude/generate.
type generateRequest struct {
	Task     string `json:"task"`     // "email" | "rewrite" | "subjects" | "spamcheck" | "command" (default: email)
	Brief    string `json:"brief"`    // descrição do que gerar / instrução de reescrita / pedido do usuário (command)
	Tone     string `json:"tone"`     // tom desejado (opcional)
	Audience string `json:"audience"` // público-alvo (opcional)
	Subject  string `json:"subject"`  // assunto a analisar (opcional em "spamcheck")
	HTML     string `json:"html"`     // conteúdo atual (obrigatório em "rewrite"/"spamcheck")
	Context  string `json:"context"`  // catálogo de ações + dados (task "command"); montado pelo painel
	// Imagem opcional (multimodal): base64 + mime. O painel sobe a foto e a manda
	// aqui; gravamos num dir temporário e o Claude a lê via Read (escopado).
	ImageBase64 string `json:"imageBase64"`
	ImageMime   string `json:"imageMime"`
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
	// task "command": uma ou mais ações escolhidas pelo LLM (o painel valida e
	// executa, em ordem) + a resposta curta em PT-BR. Action/Args ficam por
	// compatibilidade (ação única); Actions é a forma preferida (permite vários
	// passos num pedido, ex: "cria um segmento E cadastra um lead").
	Action  string            `json:"action,omitempty"`
	Args    map[string]string `json:"args,omitempty"`
	Actions []commandAction   `json:"actions,omitempty"`
	Reply   string            `json:"reply,omitempty"`
}

type commandAction struct {
	Action string            `json:"action"`
	Args   map[string]string `json:"args,omitempty"`
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

	raw, err := s.generateOnce(r.Context(), buildGeneratePrompt(req), req.ImageBase64, req.ImageMime)
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
func (s *Server) generateOnce(ctx context.Context, prompt, imageB64, imageMime string) (string, error) {
	dir, err := os.MkdirTemp(s.cfg.WorkspaceRoot, "gen-*")
	if err != nil {
		if dir, err = os.MkdirTemp("", "gen-*"); err != nil {
			return "", err
		}
	}
	defer os.RemoveAll(dir)

	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	args := []string{"-p", "--output-format", "json", "--model", s.cfg.DefaultModel}

	// Imagem anexada (multimodal): grava no dir temporário e habilita SÓ o Read,
	// escopado nesse dir (que só contém a imagem; o env já é mínimo). O Claude lê a
	// imagem antes de responder. Mantém o resto do sandbox (sem skip-permissions,
	// sem outras tools, sem MCP).
	if strings.TrimSpace(imageB64) != "" {
		ext, ok := imageExtFromMime(imageMime)
		if !ok {
			return "", fmt.Errorf("formato de imagem não suportado (use png, jpeg, webp ou gif)")
		}
		data, derr := base64.StdEncoding.DecodeString(imageB64)
		if derr != nil {
			return "", fmt.Errorf("imagem inválida: %w", derr)
		}
		if len(data) == 0 || len(data) > maxImageBytes {
			return "", fmt.Errorf("imagem vazia ou grande demais (máx 8MB)")
		}
		name := "anexo." + ext
		if werr := os.WriteFile(filepath.Join(dir, name), data, 0o600); werr != nil {
			return "", werr
		}
		// Pra o Claude LER o arquivo no modo -p, o --allowedTools sozinho não bastava
		// (ele respondia "sem acesso a ferramentas"). Usamos a mesma combinação
		// comprovada do caminho de sessão: --dangerously-skip-permissions --add-dir.
		// A diferença de segurança é o ENV: aqui é mínimo (só OAuth, SEM tokens de
		// infra) e o dir é um temp que só contém a imagem.
		args = append(args, "--dangerously-skip-permissions", "--add-dir", dir)
		prompt = "Há uma imagem anexada no arquivo ./" + name +
			" (no diretório de trabalho atual). Use a ferramenta Read para visualizá-la e leve-a em conta na resposta.\n\n" + prompt
	}

	cmd := exec.CommandContext(cctx, s.cfg.ClaudeBin, args...)
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
		fmt.Fprintf(&c, "%s\n\n", strings.TrimSpace(req.Context))
		fmt.Fprintf(&c, "Pedido do usuário: %s\n\n", strings.TrimSpace(req.Brief))

		c.WriteString("Você devolve uma LISTA de ações (campo \"actions\") que o painel executa em ordem. ")
		c.WriteString("O pedido pode pedir VÁRIAS coisas (ex.: \"cria um segmento E cadastra um lead\") — então inclua uma ação para cada parte. Se for uma só, a lista tem um item.\n\n")

		c.WriteString("Como decidir as ações:\n")
		c.WriteString("- Se for uma PERGUNTA (quantos, qual, quanto, quando, qual a taxa…), NÃO navegue. Use a ação \"answer\" e responda no campo reply USANDO os dados acima. Só diga que não sabe se o dado não estiver disponível.\n")
		c.WriteString("- Se o pedido fala em \"falha/falhou/falharam/erro/bounce\", use view_logs com args.status = \"failed\". Se fala em \"enviados/entregues\", status = \"sent\".\n")
		c.WriteString("- Use \"navigate\" SOMENTE quando o usuário pede explicitamente para abrir/ir a uma TELA, sem outra ação possível.\n")
		c.WriteString("- Se o usuário pede para CRIAR/cadastrar um lead, use create_lead (email obrigatório). Para CRIAR um segmento, use create_segment (name obrigatório; ex.: \"segmento de alunos\" → name \"Alunos\", tags \"aluno\").\n")
		c.WriteString("- Se pede para ALTERAR/descadastrar/reativar/renomear um lead, use update_lead.\n")
		c.WriteString("- Se pede para escrever/criar um email e levar pro disparo, gere assunto+HTML curtos e use prefill_compose.\n")
		c.WriteString("- Mutações (create_*/update_*) são CONFIRMADAS no painel antes de aplicar — NÃO peça pro usuário fazer manualmente nem diga que só faz uma por vez; apenas liste as ações. Preencha SEMPRE os args pedidos.\n\n")

		c.WriteString("Exemplos (pedido → JSON):\n")
		c.WriteString("\"quantos leads ativos eu tenho?\" → {\"actions\":[{\"action\":\"answer\"}],\"reply\":\"Você tem 2 leads ativos.\"}\n")
		c.WriteString("\"ver os envios que falharam\" → {\"actions\":[{\"action\":\"view_logs\",\"args\":{\"status\":\"failed\"}}],\"reply\":\"Abrindo os envios que falharam.\"}\n")
		c.WriteString("\"descadastra a ana\" → {\"actions\":[{\"action\":\"update_lead\",\"args\":{\"query\":\"ana\",\"status\":\"unsubscribed\"}}],\"reply\":\"Confirme no painel para descadastrar a Ana.\"}\n")
		c.WriteString("\"cria um segmento de alunos e cadastra o lead joao@x.com\" → {\"actions\":[{\"action\":\"create_segment\",\"args\":{\"name\":\"Alunos\",\"tags\":\"aluno\"}},{\"action\":\"create_lead\",\"args\":{\"email\":\"joao@x.com\",\"tags\":\"aluno\"}}],\"reply\":\"Vou criar o segmento Alunos e cadastrar joao@x.com — confirme no painel.\"}\n\n")

		c.WriteString("Regras de saída:\n")
		c.WriteString("- Cada item de \"actions\" tem \"action\" (id exato do catálogo, ou \"answer\") e \"args\" (só os parâmetros daquela ação, todos string; omita se não houver).\n")
		c.WriteString("- \"reply\": 1 a 2 frases em PT-BR dizendo o que fez/vai fazer ou respondendo a pergunta.\n\n")
		c.WriteString("Responda EXCLUSIVAMENTE com um JSON válido, sem cercas de código, no formato:\n")
		c.WriteString("{\"actions\": [{\"action\": \"...\", \"args\": {\"...\": \"...\"}}], \"reply\": \"...\"}\n")
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
		res.Action == "" && len(res.Actions) == 0 && res.Reply == "" {
		return nil, fmt.Errorf("resposta vazia")
	}
	return &res, nil
}
