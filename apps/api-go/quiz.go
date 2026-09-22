package main

// Orquestração da resposta de questão: Jev primeiro (barato e rápido), LLM de
// texto só quando o Jev demonstra insegurança. Os dois upstreams entram por
// injeção pra esta lógica — que é onde moram as decisões — ser testável sem
// banco, sem vault e sem rede.

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	quizSourceJev    = "jev"
	quizSourceClaude = "claude"

	// quizKindUnica/quizKindMultipla/quizKindAberta identificam o formato da
	// resposta no campo Kind: escolha única (rótulo em Answer), múltipla
	// resposta — marque todas que se aplicam (rótulos em Answers) — ou modo
	// aberto (texto livre em AnswerText, Answer vazio). Preenchido nos três
	// caminhos pra o cliente nunca precisar adivinhar por ausência de campo.
	//
	// quizKindUnica valia "multipla" antes da introdução do modo de múltipla
	// resposta — renomeado porque o nome antigo ficou errado quando
	// "multipla" passou a nomear outra coisa. Seguro: o campo subiu há pouco
	// tempo, nenhum cliente depende do valor antigo.
	quizKindUnica    = "unica"
	quizKindMultipla = "multipla"
	quizKindAberta   = "aberta"

	// ── decisão de múltipla resposta ────────────────────────────────────────
	//
	// quizMultiploThreshold: o Jev responde uma pergunta "noul" própria
	// (chave "multipla") perguntando se a questão pede mais de uma
	// alternativa. multipla >= isto classifica a questão como de múltipla
	// resposta. Abaixo disso segue o caminho de escolha única de sempre
	// (pergunta "resposta", tipo choice) — EXCETO na zona ambígua abaixo.
	quizMultiploThreshold = 0.60

	// quizMultiploAmbiguoBaixo/quizMultiploAmbiguoAlto: quando "multipla" cai
	// nessa faixa, "nem uma coisa nem outra" — a decisão única-vs-múltipla é
	// incerta demais pro Jev resolver sozinho. Escala pro Claude em modo
	// múltipla resposta (ele tolera devolver uma lista de um item quando a
	// questão é, na prática, de escolha única — ver parseFallbackAnswerMultipla).
	// Faixa se sobrepõe de propósito com o teto de quizMultiploThreshold: é a
	// mesma incerteza vista de dois ângulos (classificação vs. escalonamento).
	quizMultiploAmbiguoBaixo = 0.40
	quizMultiploAmbiguoAlto  = quizMultiploThreshold

	// quizMultiploAltGrayHigh: teto da zona cinzenta de UMA alternativa
	// individual — o piso é QUIZ_MULTI_MIN (configurável, ver quizDeps).
	// Zona cinzenta numa alternativa = "o Jev não tem certeza sobre esta
	// alternativa específica" (não sobre a questão como um todo) → escala,
	// mas só quando a questão já foi classificada como múltipla resposta.
	quizMultiploAltGrayHigh = 0.65

	// Orçamentos próprios: o API Router tem tetos largos demais pra uso
	// interativo (30s por tentativa, 60s de rotação — ver apirouter.go). O ctx
	// chega até o request do provider, então o menor prevalece. O deadline
	// curto do Jev existe pra sobrar tempo de escalar: um Jev lento não pode
	// consumir o orçamento que o fallback vai precisar.
	quizJevBudget      = 8 * time.Second
	quizFallbackBudget = 15 * time.Second
	quizTotalBudget    = 25 * time.Second

	// quizVisionBudget: visão é mais lenta que texto (o agent-go grava a
	// imagem em disco antes de ler) — medido entre 5s e 8,2s no caminho de
	// texto; imagem soma o tempo de leitura do arquivo. quizTotalBudgetImagem
	// é o orçamento total só do caminho com imagem — o de texto
	// (quizTotalBudget) continua em 25s.
	quizVisionBudget      = 40 * time.Second
	quizTotalBudgetImagem = 50 * time.Second

	// quizMaxBodyLen acompanha o teto de imagem do agent-go (maxImageBytes,
	// 8MB em handlers_generate.go) — um PNG em base64 (~4/3 do tamanho
	// decodificado) estoura qualquer teto menor. A rota é protegida por
	// authGuard (sessão válida obrigatória) + rate limit de 30/min, então um
	// corpo grande não é uma via de negação de serviço anônima.
	quizMaxBodyLen = 8 << 20

	// quizMaxImageBytes espelha maxImageBytes de apps/agent-go/handlers_generate.go
	// (não é importável entre os dois binários) — validar aqui evita gastar
	// uma chamada ao agent-go só pra ele recusar por tamanho.
	quizMaxImageBytes = 8 << 20

	// quizOpenMinChars: mínimo de caracteres não-espaço pra valer a chamada
	// ao Claude no modo aberto — abaixo disso não é uma questão, é ruído (um
	// clique errado na seleção, por exemplo).
	quizOpenMinChars = 15
	// quizOpenMaxChars: teto coerente com uma questão real de prova — bem
	// abaixo do teto de corpo (quizMaxBodyLen, 8MB), que existe pra caber
	// imagem em base64, não texto solto. Acima disso é mais provável que a
	// seleção tenha pego a página inteira do que uma questão.
	quizOpenMaxChars = 4000
)

var (
	errQuizUpstream = errors.New("quiz: nenhum modelo conseguiu responder")
	errQuizTimeout  = errors.New("quiz: tempo esgotado")

	// errQuizImagemMimeInvalido, errQuizImagemBase64Invalido e
	// errQuizImagemGrandeDemais: validação da imagem ANTES de gastar uma
	// chamada de rede — mime fora da allowlist do agent-go, base64
	// malformado (não confundir com mime errado — são causas diferentes, e
	// misturar as duas manda quem depura atrás do campo errado), ou tamanho
	// decodificado acima do limite dele.
	errQuizImagemMimeInvalido   = errors.New("quiz: mime de imagem não suportado (use png, jpeg, webp ou gif)")
	errQuizImagemBase64Invalido = errors.New("quiz: base64 da imagem malformado")
	errQuizImagemGrandeDemais   = errors.New("quiz: imagem decodificada maior que o limite")

	// errQuizTextoInsuficiente: guarda do modo aberto — texto vazio, curto
	// demais (ruído) ou longo demais (provavelmente a página inteira, não a
	// questão) não vale gastar uma chamada ao Claude. Sentinela própria (não
	// errQuizUnparseable): a causa aqui é tamanho de texto, não ausência de
	// rótulos — misturar as duas confundiria quem depura pelo código do erro.
	errQuizTextoInsuficiente = errors.New("quiz: texto insuficiente para responder no modo aberto")
)

// quizImagemMimesAceitos espelha imageExtFromMime de
// apps/agent-go/handlers_generate.go — os únicos mimes que o agent-go sabe
// gravar e ler de volta pro Claude.
var quizImagemMimesAceitos = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

type quizRequest struct {
	Raw      string            `json:"raw"`
	Question string            `json:"question"`
	Options  map[string]string `json:"options"`
	Explain  bool              `json:"explain"`
	// Imagem opcional: a extensão manda a figura (gráfico, cupom, tabela,
	// figura geométrica) que a questão referencia, quando o enunciado
	// sozinho não basta pra responder. Presente → pula direto pro fallback
	// com visão (ver answerQuiz).
	ImageBase64 string `json:"imageBase64"`
	ImageMime   string `json:"imageMime"`
}

// validateQuizImage confere o mime (allowlist) e o tamanho decodificado
// ANTES de qualquer chamada de rede — gastar uma tentativa de upstream só
// pra descobrir que a imagem é grande ou de um tipo não suportado seria
// desperdiçar o orçamento de tempo que o vision budget já é apertado.
func validateQuizImage(imageB64, mime string) error {
	if !quizImagemMimesAceitos[mime] {
		return errQuizImagemMimeInvalido
	}
	decoded, err := base64.StdEncoding.DecodeString(imageB64)
	if err != nil {
		return errQuizImagemBase64Invalido
	}
	if len(decoded) > quizMaxImageBytes {
		return errQuizImagemGrandeDemais
	}
	return nil
}

// validateQuizOpenText confere o texto do modo aberto ANTES de gastar uma
// chamada ao Claude — mesma motivação de validateQuizImage: falhar na hora é
// mais barato que deixar o upstream recusar depois.
func validateQuizOpenText(raw string) error {
	semEspaco := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)
	if len([]rune(semEspaco)) < quizOpenMinChars {
		return errQuizTextoInsuficiente
	}
	if len([]rune(raw)) > quizOpenMaxChars {
		return errQuizTextoInsuficiente
	}
	return nil
}

type quizTimings struct {
	JevMs    int64 `json:"jevMs"`
	ClaudeMs int64 `json:"claudeMs"`
	TotalMs  int64 `json:"totalMs"`
}

type quizResponse struct {
	// Kind: "unica" (Answer traz o rótulo, AnswerText o texto da
	// alternativa), "multipla" (Answer/AnswerText vazios, Answers traz os
	// rótulos marcados e AnswerProbs a probabilidade de cada alternativa) ou
	// "aberta" (Answer vazio — não há rótulo —, AnswerText traz a resposta em
	// texto livre). Preenchido nos três caminhos.
	Kind       string `json:"kind"`
	Answer     string `json:"answer"`
	AnswerText string `json:"answerText"`
	// Answers/AnswerProbs: só preenchidos quando Kind é "multipla" — rótulos
	// marcados (em ordem de p.Order) e a probabilidade de CADA alternativa
	// (não só as marcadas), pra o card mostrar o número mesmo da que ficou de
	// fora. Ausentes (omitempty) nos outros modos — Answer/AnswerText já
	// cobrem o resultado ali, e publicar um map/slice vazio seria ruído.
	Answers       []string           `json:"answers,omitempty"`
	AnswerProbs   map[string]float64 `json:"answerProbs,omitempty"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Source        string             `json:"source"`
	Escalated     bool               `json:"escalated"`
	Degraded      bool               `json:"degraded"`
	Reasoning     string             `json:"reasoning,omitempty"`
	Parsed        quizParsed         `json:"parsed"`
	Timings       quizTimings        `json:"timings"`
}

type quizDeps struct {
	jev func(ctx context.Context, body []byte) ([]byte, error)
	// fallback devolve o texto cru do Claude Code (apps/agent-go via
	// agent_client.go), não envelope de provider — sem chave de API, o
	// container roda com a assinatura da empresa. imageB64/imageMime vazios
	// quando a questão não tem figura anexada (caminho de texto).
	fallback      func(ctx context.Context, prompt, imageB64, imageMime string) (string, error)
	minConfidence float64
	minMargin     float64
	// minMultiAlt: piso de probabilidade (QUIZ_MULTI_MIN, default 0.45) pra
	// marcar uma alternativa no modo múltipla resposta. Deliberadamente
	// baixo — decisão do dono do projeto: na dúvida, marcar a mais. O
	// usuário vê a probabilidade no card e desmarca; esconder uma
	// alternativa que valia ponto é o erro mais caro dos dois.
	minMultiAlt float64
}

func answerQuiz(ctx context.Context, req quizRequest, deps quizDeps) (quizResponse, error) {
	started := time.Now()

	temImagem := req.ImageBase64 != ""
	if temImagem {
		// Valida ANTES de abrir o orçamento de tempo e de chamar qualquer
		// upstream — mime errado ou imagem grande demais tem que falhar na
		// hora, não gastar a chamada só pra o agent-go recusar depois.
		if err := validateQuizImage(req.ImageBase64, req.ImageMime); err != nil {
			return quizResponse{}, err
		}
	}

	totalBudget := quizTotalBudget
	if temImagem {
		totalBudget = quizTotalBudgetImagem
	}
	ctx, cancel := context.WithTimeout(ctx, totalBudget)
	defer cancel()

	parsed, err := resolveQuizParsed(req)
	if err != nil {
		// Modo aberto: só entra aqui quando a falha veio do bloco cru sem
		// nenhuma alternativa reconhecível — texto de preencher lacuna ou
		// questão aberta, que hoje tomava 422 à toa. `options` mal-formado
		// (fora do limite de 2–9) continua erro: ali o cliente já tentou
		// separar alternativas e errou o formato, não é o caso "sem
		// alternativa nenhuma".
		if len(req.Options) == 0 && errors.Is(err, errQuizUnparseable) {
			return answerQuizAberto(ctx, req, deps, started, temImagem)
		}
		return quizResponse{}, err
	}

	resp := quizResponse{Parsed: parsed, Kind: quizKindUnica}

	if temImagem {
		// O Jev não lê imagem — chamá-lo às cegas só gastaria tempo e
		// dinheiro num palpite que ignora a figura. Vai direto pro fallback
		// com visão, sempre escalado.
		ans, claudeMs, fbErr := askFallback(ctx, parsed, req.ImageBase64, req.ImageMime, deps)
		resp.Timings.ClaudeMs = claudeMs
		if fbErr != nil {
			// Sem veredito do Jev, não há palpite nenhum pra degradar: a
			// figura é indispensável pra responder, então isto é erro, não
			// uma resposta fraca.
			if ctx.Err() != nil {
				return quizResponse{}, errQuizTimeout
			}
			return quizResponse{}, errQuizUpstream
		}
		resp.Source = quizSourceClaude
		resp.Escalated = true
		resp.Answer = ans.Label
		resp.AnswerText = parsed.Options[ans.Label]
		resp.Reasoning = ans.Reasoning
		// Confidence/Probabilities ficam no zero-value: são do Jev, e ele
		// nem chegou a opinar — mesma regra já aplicada quando o Jev falha
		// no caminho de texto (ver abaixo).
		resp.Timings.TotalMs = time.Since(started).Milliseconds()
		return resp, nil
	}

	verdict, jevMs, jevErr := askJev(ctx, parsed, deps)
	resp.Timings.JevMs = jevMs

	if jevErr == nil {
		// provavelMultipla/multiplaAmbigua: ver o comentário de
		// quizMultiploThreshold/quizMultiploAmbiguoBaixo — a faixa ambígua
		// sobrepõe de propósito o teto da faixa "é múltipla".
		provavelMultipla := verdict.MultiplaProb >= quizMultiploThreshold
		multiplaAmbigua := verdict.MultiplaProb >= quizMultiploAmbiguoBaixo && verdict.MultiplaProb <= quizMultiploAmbiguoAlto
		if provavelMultipla || multiplaAmbigua {
			// Escala quando a decisão única-vs-múltipla está incerta
			// (multiplaAmbigua) OU quando a questão já é claramente de
			// múltipla resposta mas alguma alternativa específica está na
			// zona cinzenta — nesse segundo caso só faz sentido checar zona
			// cinzenta de alternativa se a questão É múltipla mesmo.
			escalar := multiplaAmbigua || (provavelMultipla && algumaAltNaZonaCinzenta(verdict.AltProbs, deps.minMultiAlt))
			return answerQuizMultipla(ctx, parsed, verdict, jevMs, deps, started, escalar)
		}
	}

	escalate := req.Explain || jevErr != nil ||
		verdict.Confidence < deps.minConfidence ||
		verdict.margin() < deps.minMargin

	if !escalate {
		fillFromJev(&resp, verdict, parsed)
		resp.Timings.TotalMs = time.Since(started).Milliseconds()
		return resp, nil
	}

	ans, claudeMs, fbErr := askFallback(ctx, parsed, "", "", deps)
	resp.Timings.ClaudeMs = claudeMs
	switch {
	case fbErr == nil:
		resp.Source = quizSourceClaude
		resp.Escalated = true
		resp.Answer = ans.Label
		resp.AnswerText = parsed.Options[ans.Label]
		resp.Reasoning = ans.Reasoning
		// Confiança e probabilidades são do Jev: só fazem sentido se ele
		// chegou a responder. Quando ele falhou, copiá-las publicaria o
		// zero-value como se fosse "0% de confiança" numa resposta que o
		// fallback acertou.
		if jevErr == nil {
			resp.Confidence = verdict.Confidence
			resp.Probabilities = verdict.Probabilities
		}
	case jevErr == nil:
		// Degradação: o fallback morreu, mas o palpite do Jev existe. Devolver
		// palpite fraco é melhor que devolver erro no meio de uma questão.
		// Escalated fica false: o campo descreve de onde veio a resposta lida
		// agora (o Jev, estágio não-escalado), não se uma tentativa de
		// escalar aconteceu — isso é o que Degraded sinaliza.
		fillFromJev(&resp, verdict, parsed)
		resp.Degraded = true
	default:
		if ctx.Err() != nil {
			return quizResponse{}, errQuizTimeout
		}
		return quizResponse{}, errQuizUpstream
	}
	resp.Timings.TotalMs = time.Since(started).Milliseconds()
	return resp, nil
}

func fillFromJev(resp *quizResponse, v quizVerdict, p quizParsed) {
	resp.Source = quizSourceJev
	resp.Answer = v.Label
	resp.AnswerText = p.Options[v.Label]
	resp.Confidence = v.Confidence
	resp.Probabilities = v.Probabilities
}

// resolveQuizParsed aceita os dois formatos de entrada: bloco cru (o caminho
// normal) ou alternativas já separadas pelo cliente.
func resolveQuizParsed(req quizRequest) (quizParsed, error) {
	if len(req.Options) > 0 {
		// Mesmos limites que parseQuizBlock aplica no bloco cru: sem isso, o
		// caminho "alternativas já separadas" aceitaria 1 ou 15 alternativas
		// sem reclamar.
		if len(req.Options) < quizMinOptions || len(req.Options) > quizMaxOptions {
			return quizParsed{}, errQuizUnparseable
		}
		order := make([]string, 0, len(req.Options))
		for label := range req.Options {
			order = append(order, label)
		}
		// Mapa em Go não tem ordem: sem ordenar, o prompt do fallback sairia
		// embaralhado a cada requisição e a resposta mudaria sozinha.
		sort.Strings(order)
		return quizParsed{Question: req.Question, Options: req.Options, Order: order}, nil
	}
	return parseQuizBlock(req.Raw)
}

func askJev(ctx context.Context, p quizParsed, deps quizDeps) (quizVerdict, int64, error) {
	body, err := buildJevRequest(p)
	if err != nil {
		return quizVerdict{}, 0, err
	}
	jevCtx, cancel := context.WithTimeout(ctx, quizJevBudget)
	defer cancel()
	started := time.Now()
	raw, err := deps.jev(jevCtx, body)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return quizVerdict{}, elapsed, err
	}
	v, err := parseJevResponse(raw, p)
	return v, elapsed, err
}

func askFallback(ctx context.Context, p quizParsed, imageB64, imageMime string, deps quizDeps) (quizFallbackAnswer, int64, error) {
	temImagem := imageB64 != ""
	budget := quizFallbackBudget
	if temImagem {
		budget = quizVisionBudget
	}
	fbCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	started := time.Now()
	texto, err := deps.fallback(fbCtx, buildFallbackPrompt(p, temImagem), imageB64, imageMime)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return quizFallbackAnswer{}, elapsed, err
	}
	ans, err := parseFallbackAnswer(texto, p)
	return ans, elapsed, err
}

// answerQuizAberto responde questões sem alternativas reconhecíveis (aberta
// ou preencher lacuna): manda o texto direto ao Claude e devolve a resposta
// em texto livre. Nunca chama o Jev — ele só sabe escolher entre opções, e
// aqui não há opções pra escolher. Funciona com ou sem imagem (temImagem
// controla o orçamento de tempo e se a imagem é repassada ao fallback).
func answerQuizAberto(ctx context.Context, req quizRequest, deps quizDeps, started time.Time, temImagem bool) (quizResponse, error) {
	texto := strings.TrimSpace(req.Raw)
	if err := validateQuizOpenText(texto); err != nil {
		return quizResponse{}, err
	}

	resp := quizResponse{
		Kind: quizKindAberta,
		// Options vazio (não nil): o contrato da rota exige o campo como
		// objeto — ver docs/openapi.yaml — e não há alternativa nenhuma pra
		// listar no modo aberto.
		Parsed: quizParsed{Question: texto, Options: map[string]string{}},
	}

	var imageB64, imageMime string
	if temImagem {
		imageB64, imageMime = req.ImageBase64, req.ImageMime
	}

	ans, claudeMs, fbErr := askOpenFallback(ctx, texto, imageB64, imageMime, deps)
	resp.Timings.ClaudeMs = claudeMs
	if fbErr != nil {
		// Sem Jev nesse caminho, não há palpite nenhum pra degradar — igual
		// ao caminho com imagem da múltipla escolha.
		if ctx.Err() != nil {
			return quizResponse{}, errQuizTimeout
		}
		return quizResponse{}, errQuizUpstream
	}
	resp.Source = quizSourceClaude
	resp.Escalated = true
	// Answer fica vazio de propósito: não existe rótulo no modo aberto — a
	// resposta inteira mora em AnswerText.
	resp.AnswerText = ans.Answer
	resp.Reasoning = ans.Reasoning
	resp.Timings.TotalMs = time.Since(started).Milliseconds()
	return resp, nil
}

// ── modo múltipla resposta ("marque todas que se aplicam") ────────────────

// answerQuizMultipla responde questões de múltipla resposta. Chamada só
// quando o sinal "multipla" do Jev (verdict.MultiplaProb) indicou que a
// questão é ou pode ser de múltipla resposta — ver a decisão em answerQuiz.
// Espelha o fluxo de escolha única (fillFromJev/askFallback), mas decide por
// limiar sobre AltProbs em vez de escolher uma única alternativa, e não
// chama o Jev de novo (o mesmo veredito já tem tudo: multipla e alt_X vêm da
// mesma chamada que "resposta").
//
// jevMs vem já medido de answerQuiz (a chamada ao Jev que produziu verdict) —
// answerQuizMultipla monta um quizResponse próprio (não reaproveita o do
// caminho de escolha única), então precisa receber o valor por parâmetro e
// gravá-lo aqui. Sem isso Timings.JevMs fica no zero-value nesta resposta
// mesmo quando o Jev foi chamado e respondeu (bug de produção: totalMs > 0,
// jevMs == 0 numa resposta source=jev).
func answerQuizMultipla(ctx context.Context, p quizParsed, verdict quizVerdict, jevMs int64, deps quizDeps, started time.Time, escalar bool) (quizResponse, error) {
	resp := quizResponse{Parsed: p, Kind: quizKindMultipla}
	resp.Timings.JevMs = jevMs

	if !escalar {
		resp.Source = quizSourceJev
		resp.Answers = selectQuizMultiplaAnswers(verdict.AltProbs, p.Order, deps.minMultiAlt)
		resp.AnswerProbs = verdict.AltProbs
		resp.Timings.TotalMs = time.Since(started).Milliseconds()
		return resp, nil
	}

	ans, claudeMs, fbErr := askFallbackMultipla(ctx, p, "", "", deps)
	resp.Timings.ClaudeMs = claudeMs
	if fbErr != nil {
		// Degradação: mesmo sem sucesso na escalada, o Jev tem um palpite (os
		// alt_X já calculados) — um palpite fraco no meio de uma questão vale
		// mais que uma tela de erro. Mesmo raciocínio do caminho de escolha
		// única (fillFromJev + Degraded).
		resp.Source = quizSourceJev
		resp.Answers = selectQuizMultiplaAnswers(verdict.AltProbs, p.Order, deps.minMultiAlt)
		resp.AnswerProbs = verdict.AltProbs
		resp.Degraded = true
		resp.Timings.TotalMs = time.Since(started).Milliseconds()
		return resp, nil
	}
	resp.Source = quizSourceClaude
	resp.Escalated = true
	resp.Answers = ans.Labels
	// AnswerProbs continua vindo do Jev mesmo escalado: só ele calcula
	// probabilidade por alternativa — o Claude devolve só a lista marcada,
	// sem número nenhum pra publicar no lugar.
	resp.AnswerProbs = verdict.AltProbs
	resp.Reasoning = ans.Reasoning
	resp.Timings.TotalMs = time.Since(started).Milliseconds()
	return resp, nil
}

// algumaAltNaZonaCinzenta confere se alguma alternativa está entre minAlt
// (QUIZ_MULTI_MIN) e quizMultiploAltGrayHigh — "o Jev não tem certeza sobre
// esta alternativa específica", motivo suficiente pra escalar mesmo com a
// questão já classificada como múltipla resposta.
func algumaAltNaZonaCinzenta(probs map[string]float64, minAlt float64) bool {
	for _, p := range probs {
		if p >= minAlt && p <= quizMultiploAltGrayHigh {
			return true
		}
	}
	return false
}

// selectQuizMultiplaAnswers marca as alternativas com AltProbs >= minAlt, na
// ordem de p.Order. minAlt deliberadamente baixo (QUIZ_MULTI_MIN, default
// 0.45) — ver o comentário de quizDeps.minMultiAlt. Nunca devolve lista
// vazia: sem nenhuma alternativa acima do limiar, cai para a de maior
// probabilidade (maxProbLabel, mesmo desempate determinístico de quiz_jev.go).
func selectQuizMultiplaAnswers(probs map[string]float64, order []string, minAlt float64) []string {
	var marcadas []string
	for _, label := range order {
		if probs[label] >= minAlt {
			marcadas = append(marcadas, label)
		}
	}
	if len(marcadas) > 0 {
		return marcadas
	}
	melhor := maxProbLabel(probs)
	if melhor == "" {
		return nil
	}
	return []string{melhor}
}

func askFallbackMultipla(ctx context.Context, p quizParsed, imageB64, imageMime string, deps quizDeps) (quizFallbackAnswerMultipla, int64, error) {
	temImagem := imageB64 != ""
	budget := quizFallbackBudget
	if temImagem {
		budget = quizVisionBudget
	}
	fbCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	started := time.Now()
	texto, err := deps.fallback(fbCtx, buildFallbackPromptMultipla(p, temImagem), imageB64, imageMime)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return quizFallbackAnswerMultipla{}, elapsed, err
	}
	ans, err := parseFallbackAnswerMultipla(texto, p)
	return ans, elapsed, err
}

func askOpenFallback(ctx context.Context, texto, imageB64, imageMime string, deps quizDeps) (quizOpenAnswer, int64, error) {
	temImagem := imageB64 != ""
	budget := quizFallbackBudget
	if temImagem {
		budget = quizVisionBudget
	}
	fbCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	started := time.Now()
	resposta, err := deps.fallback(fbCtx, buildOpenPrompt(texto, temImagem), imageB64, imageMime)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return quizOpenAnswer{}, elapsed, err
	}
	ans, err := parseOpenAnswer(resposta)
	return ans, elapsed, err
}
