package main

import (
	"bufio"
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
	// Opções por geração (barra do assistente dos quadros, task "diagram").
	Web   bool     `json:"web"`   // libera WebSearch/WebFetch nesta geração
	Model string   `json:"model"` // override do modelo: "sonnet" | "opus" | "haiku" ("" = default)
	Kind  string   `json:"kind"`  // tipo do diagrama: "" auto | "flowchart" | "sequence" | "class"
	Tools []string `json:"tools"` // conectores MCP liberados: "santos" | "notion" | "miro"
}

// diagramTools mapeia a opção da barra pro prefixo --allowedTools do conector
// claude.ai correspondente (conta conectada no container do agent).
var diagramTools = map[string]string{
	"santos": "mcp__claude_ai_Santos_Tech",
	"notion": "mcp__claude_ai_Notion",
	"miro":   "mcp__claude_ai_Miro",
}

// toolCallRecord captura um uso de ferramenta durante geração do LLM.
type toolCallRecord struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
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
	// task "diagram": código Mermaid gerado (o quadro converte pra elementos
	// Excalidraw no front).
	Mermaid string `json:"mermaid,omitempty"`
	// task "raw": tool calls capturados durante a geração (pode ser nil).
	ToolCalls []toolCallRecord `json:"toolCalls,omitempty"`
}

type commandAction struct {
	Action string            `json:"action"`
	Args   map[string]string `json:"args,omitempty"`
}

// handleGenerate gera conteúdo de email/template num único turno (stateless): não
// cria conversa, não persiste nada. Roda o Claude com ambiente mínimo (só OAuth).
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := decodeJSONLimit(r, &req, maxGenerateBody); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if err := normalizeGenerate(&req); err != nil {
		writeErr(w, err)
		return
	}

	// task "raw": passa o brief diretamente ao Claude sem nenhum sistema de prompt.
	// Usado por serviços internos (ex: bot-atendimento) que montam o próprio prompt.
	// Usa stream-json para capturar tool calls emitidos durante a geração.
	if req.Task == "raw" {
		if strings.TrimSpace(req.Brief) == "" {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "brief obrigatório para task raw"))
			return
		}
		raw, toolCalls, err := s.generateOnceWithTrace(r.Context(), req.Task, req.Brief, req.Model, req.Web)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, &generateResult{Text: raw, ToolCalls: toolCalls})
		return
	}

	raw, err := s.generateOnce(r.Context(), req.Task, buildGeneratePrompt(req, strings.TrimSpace(req.ImageBase64) != ""), req.ImageBase64, req.ImageMime, req.Model, req.Web, req.Tools)
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
// texto final (campo .result). Sem --add-dir, sem skip-permissions e sem tokens
// de infra no ambiente — é redação de texto, não automação. `model` vazio usa o
// default; `web` libera SÓ WebSearch/WebFetch; `tools` libera os conectores MCP
// da conta (diagramTools) — ambos com timeout maior.
func (s *Server) generateOnce(ctx context.Context, task, prompt, imageB64, imageMime, model string, web bool, tools []string) (string, error) {
	dir, err := os.MkdirTemp(s.cfg.WorkspaceRoot, "gen-*")
	if err != nil {
		if dir, err = os.MkdirTemp("", "gen-*"); err != nil {
			return "", err
		}
	}
	defer os.RemoveAll(dir)

	// Busca na web e conectores MCP demoram mais (vários turnos) — timeout maior.
	timeout := 90 * time.Second
	if web || len(tools) > 0 {
		timeout = 240 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if model == "" {
		model = s.cfg.DefaultModel
	}
	args := []string{"-p", "--output-format", "json", "--model", model}
	var allowed []string
	if web {
		allowed = append(allowed, "WebSearch", "WebFetch")
	}
	for _, t := range tools {
		if prefix, ok := diagramTools[t]; ok {
			allowed = append(allowed, prefix)
		}
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}

	// Imagem anexada (multimodal): grava no dir temporário e pede o Read escopado
	// nesse dir (que só contém a imagem). O Claude lê a imagem antes de responder.
	// ATENÇÃO: este caminho ADICIONA --dangerously-skip-permissions (ver abaixo) —
	// não é um sandbox "sem skip-permissions". O que continua valendo é: sem MCP,
	// env mínimo (allow-list de claudeEnv) e --add-dir restrito ao temp da imagem.
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
		// A flag é intencional (o Claude ter ferramentas é o produto); a contenção
		// vem do ENV mínimo (claudeEnv: sem JWT_SECRET/DATABASE_URL/tokens de infra),
		// do dir temp que só contém a imagem e do guard admin-only da rota.
		args = append(args, "--dangerously-skip-permissions", "--add-dir", dir)
		prompt = "Há uma imagem anexada no arquivo ./" + name +
			" (no diretório de trabalho atual). Use a ferramenta Read para visualizá-la e leve-a em conta na resposta.\n\n" + prompt
	}

	cmd := exec.CommandContext(cctx, s.cfg.ClaudeBin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)

	// Ambiente mínimo e EXPLÍCITO (allow-list de claudeEnv): runtime essencial +
	// token OAuth da assinatura. conv=nil => sem repo clonado => sem GITHUB_TOKEN.
	// Antes isto era os.Environ(), que vazava JWT_SECRET/DATABASE_URL/tokens de
	// infra para dentro do processo Claude (exfiltráveis via prompt injection).
	cmd.Env = s.claudeEnv(ctx, nil)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr // logs do CLI vão pro stderr do serviço
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude falhou: %w", err)
	}

	// --output-format json: um envelope com o texto final em .result e o custo em
	// .total_cost_usd/.usage — decodifica como mapa pra extrair os dois sem duplicar
	// a leitura do stdout (fica registrado mesmo quando is_error=true).
	var ev map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &ev); err != nil {
		return "", fmt.Errorf("resposta do claude inválida: %w", err)
	}
	f := usageFromMap(ev)
	s.recordUsage(ctx, "generate", task, model, "", f)

	result, _ := ev["result"].(string)
	subtype, _ := ev["subtype"].(string)
	if f.IsError || strings.TrimSpace(result) == "" {
		return "", fmt.Errorf("claude não retornou resultado (subtype=%s)", subtype)
	}
	return result, nil
}

// generateOnceWithTrace é como generateOnce mas usa stream-json para capturar
// tool calls emitidos pelo Claude durante a geração. Retorna o texto final e
// a lista de tool calls (pode ser nil se nenhuma ferramenta foi usada).
func (s *Server) generateOnceWithTrace(ctx context.Context, task, prompt, model string, web bool) (string, []toolCallRecord, error) {
	dir, err := os.MkdirTemp(s.cfg.WorkspaceRoot, "gen-*")
	if err != nil {
		if dir, err = os.MkdirTemp("", "gen-*"); err != nil {
			return "", nil, err
		}
	}
	defer os.RemoveAll(dir)

	timeout := 180 * time.Second
	if web {
		timeout = 300 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if model == "" {
		model = s.cfg.DefaultModel
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--model", model}
	if web {
		args = append(args, "--allowedTools", "WebSearch,WebFetch")
	}

	cmd := exec.CommandContext(cctx, s.cfg.ClaudeBin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)

	// Mesmo ambiente mínimo do generateOnce (allow-list explícita, sem segredos
	// de infra). Ver claudeEnv em session.go.
	cmd.Env = s.claudeEnv(ctx, nil)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("claude falhou: %w", err)
	}

	var finalText string
	var toolCalls []toolCallRecord
	var usage usageFields

	sc := bufio.NewScanner(bytes.NewReader(stdout.Bytes()))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
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
		case "assistant":
			for _, block := range messageContent(ev) {
				if block["type"] != "tool_use" {
					continue
				}
				name, _ := block["name"].(string)
				if name == "" {
					continue
				}
				var inputRaw json.RawMessage
				if inp, ok := block["input"]; ok {
					inputRaw, _ = json.Marshal(inp)
				}
				toolCalls = append(toolCalls, toolCallRecord{Name: name, Input: inputRaw})
			}
		case "result":
			usage = usageFromMap(ev)
			if rs, _ := ev["result"].(string); rs != "" {
				finalText = rs
			}
		}
	}
	// O evento "result" só sai se o CLI chegou ao fim do turno — se o processo
	// morreu antes (timeout, kill), não há custo a registrar.
	if usage != (usageFields{}) {
		s.recordUsage(ctx, "generate", task, model, "", usage)
	}

	if finalText == "" {
		return "", nil, fmt.Errorf("claude não retornou resultado")
	}
	return finalText, toolCalls, nil
}

// buildGeneratePrompt monta a instrução conforme a tarefa. Em todos os casos o
// modelo é instruído a responder EXCLUSIVAMENTE com JSON (sem cercas, sem ferramentas).
func buildGeneratePrompt(req generateRequest, hasImage bool) string {
	tone := strings.TrimSpace(req.Tone)
	if tone == "" {
		tone = "profissional, claro e acolhedor"
	}
	audience := strings.TrimSpace(req.Audience)
	if audience == "" {
		audience = "leads da Santos Tech"
	}

	// Restrição de ferramentas: por padrão NENHUMA; mas com imagem anexada o modelo
	// PRECISA do Read pra vê-la — senão ele obedece o "não use ferramentas" e recusa.
	toolRule := "NÃO use ferramentas, NÃO acesse arquivos e NÃO rode comandos."
	if hasImage {
		toolRule = "Use a ferramenta Read para abrir a imagem anexada (arquivo no diretório de trabalho atual) ANTES de responder. Não use nenhuma outra ferramenta."
	}

	// A task "command" tem um sistema de prompt próprio (assistente do painel, não
	// redator), então é tratada antes do cabeçalho de redação.
	if req.Task == "command" {
		var c strings.Builder
		c.WriteString("Você é o assistente do painel de email da Santos Tech. Responda em português do Brasil.\n")
		c.WriteString(toolRule + " Sua única saída é um JSON.\n\n")
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

	// A task "diagram" gera código Mermaid pro editor de quadros (Excalidraw)
	// do dashboard — também não é redação de email, então sai antes.
	if req.Task == "diagram" {
		var d strings.Builder
		d.WriteString("Você gera diagramas em código Mermaid para um quadro branco (Excalidraw).\n")
		// Monta a regra de ferramentas conforme as opções liberadas.
		var can []string
		if hasImage {
			can = append(can, "Read (para abrir a imagem anexada ANTES de responder)")
		}
		if req.Web {
			can = append(can, "WebSearch e WebFetch (para pesquisar quando o pedido depender de informação que você não tem certeza)")
		}
		for _, t := range req.Tools {
			switch t {
			case "santos":
				can = append(can, "as ferramentas do MCP Santos Tech (dados internos do ecossistema: usuários, emails, métricas)")
			case "notion":
				can = append(can, "as ferramentas do MCP Notion (páginas e bancos do workspace)")
			case "miro":
				can = append(can, "as ferramentas do MCP Miro (boards do Miro)")
			}
		}
		if len(can) > 0 {
			d.WriteString("Você PODE usar: " + strings.Join(can, "; ") + ". Nenhuma outra ferramenta. Consulte os dados de que precisar ANTES de montar o diagrama — não invente o que dá pra consultar. Sua única saída é um JSON.\n\n")
		} else {
			d.WriteString(toolRule + " Sua única saída é um JSON.\n\n")
		}
		if hasImage {
			d.WriteString("A imagem anexada é uma captura do estado ATUAL do quadro — leve em conta o que já está desenhado (complete, conecte ou converta conforme o pedido).\n\n")
		}
		if ctx := strings.TrimSpace(req.Context); ctx != "" {
			fmt.Fprintf(&d, "Diagrama Mermaid atual:\n%s\n\nAltere o diagrama acima conforme a instrução, preservando o que não foi pedido pra mudar.\nInstrução: %s\n\n", ctx, strings.TrimSpace(req.Brief))
		} else {
			fmt.Fprintf(&d, "Crie um diagrama para o pedido abaixo.\nPedido: %s\n\n", strings.TrimSpace(req.Brief))
		}
		d.WriteString("Regras do Mermaid:\n")
		switch req.Kind {
		case "flowchart":
			d.WriteString("- Use OBRIGATORIAMENTE flowchart (graph TD ou LR).\n")
		case "sequence":
			d.WriteString("- Use OBRIGATORIAMENTE sequenceDiagram.\n")
		case "class":
			d.WriteString("- Use OBRIGATORIAMENTE classDiagram.\n")
		default:
			d.WriteString("- Use flowchart (graph TD/LR) por padrão; sequenceDiagram ou classDiagram só se o pedido indicar.\n")
		}
		d.WriteString("- Rótulos em português do Brasil, curtos e claros.\n")
		d.WriteString("- Cores: quando o pedido mencionar cores OU quando colorir ajudar a distinguir grupos/estados, use classDef com fill e stroke (só em flowchart). Paleta preferida: azul #187ABF, verde #0DB88F, azul-escuro #04325A, apoio #0067BE; vermelho/âmbar só para erro/aviso. Com fundo escuro (#04325A), use color:#ffffff. Sem pedido e sem ganho claro, não estilize.\n")
		d.WriteString("- Sem linkStyle e sem comentários.\n")
		d.WriteString("- O código precisa ser Mermaid válido; rótulos com parênteses, vírgulas ou acentos vão entre aspas duplas.\n\n")
		d.WriteString("Responda EXCLUSIVAMENTE com um JSON válido, sem cercas de código, no formato:\n")
		d.WriteString("{\"mermaid\": \"graph TD\\n  A[\\\"Início\\\"] --> B[\\\"Fim\\\"]\"}\n")
		return d.String()
	}

	var b strings.Builder
	b.WriteString("Você é um redator de email marketing da Santos Tech.\n")
	b.WriteString("Responda em português do Brasil. " + toolRule + "\n\n")

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
// de código e texto ao redor.
//
// (#6) Antes fatiávamos do 1º "{" ao ÚLTIMO "}". Isso corrompe uma resposta JSON
// válida que tenha um "}" depois do objeto (ex.: o modelo escreve o JSON e adiciona
// uma frase com "}", ou um segundo bloco) — o slice englobava lixo e o Unmarshal
// falhava. Agora usamos um json.Decoder a partir do 1º "{": ele lê o PRIMEIRO objeto
// JSON válido e para, ignorando qualquer coisa depois.
func parseGenerateResult(raw string) (*generateResult, error) {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '{'); i >= 0 {
		s = s[i:]
	}
	var res generateResult
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&res); err != nil {
		return nil, err
	}
	if res.Subject == "" && res.HTML == "" && res.Text == "" && len(res.Subjects) == 0 &&
		res.Score == nil && res.Summary == "" && len(res.Issues) == 0 &&
		res.Action == "" && len(res.Actions) == 0 && res.Reply == "" && res.Mermaid == "" {
		return nil, fmt.Errorf("resposta vazia")
	}
	return &res, nil
}
