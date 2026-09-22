package main

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func depsFake(jevBody string, jevErr error, fbText string, fbErr error, chamadas *[]string) quizDeps {
	return quizDeps{
		jev: func(ctx context.Context, body []byte) ([]byte, error) {
			*chamadas = append(*chamadas, "jev")
			if jevErr != nil {
				return nil, jevErr
			}
			return []byte(jevBody), nil
		},
		fallback: func(ctx context.Context, prompt, imageB64, imageMime string) (string, error) {
			*chamadas = append(*chamadas, "fallback")
			if fbErr != nil {
				return "", fbErr
			}
			return fbText, nil
		},
		minConfidence: 0.75,
		minMargin:     0.15,
	}
}

const (
	jevConfiante = `{"answers":{"resposta":{"type":"choice","choice":"B","confidence":0.93,"probabilities":{"A":0.02,"B":0.93,"C":0.05}}}}`
	jevInseguro  = `{"answers":{"resposta":{"type":"choice","choice":"B","confidence":0.41,"probabilities":{"A":0.39,"B":0.41,"C":0.20}}}}`
	jevEmpatado  = `{"answers":{"resposta":{"type":"choice","choice":"B","confidence":0.90,"probabilities":{"A":0.88,"B":0.90}}}}`
	fbOK         = `{"answer":"A","reasoning":"porque sim"}`
)

func reqExemplo() quizRequest {
	return quizRequest{Raw: "Qual a capital da Mongólia?\nA) Astana\nB) Ulan Bator\nC) Bishkek"}
}

func TestAnswerQuizJevConfianteNaoEscala(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceJev || got.Escalated {
		t.Errorf("source=%q escalated=%v", got.Source, got.Escalated)
	}
	if got.Answer != "B" || got.AnswerText != "Ulan Bator" {
		t.Errorf("resposta = %q / %q", got.Answer, got.AnswerText)
	}
	if len(chamadas) != 1 || chamadas[0] != "jev" {
		t.Errorf("chamadas = %v, queria só o jev (escalar à toa custa dinheiro e tempo)", chamadas)
	}
}

func TestAnswerQuizConfiancaBaixaEscala(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevInseguro, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceClaude || !got.Escalated {
		t.Errorf("source=%q escalated=%v", got.Source, got.Escalated)
	}
	if got.Answer != "A" || got.Reasoning == "" {
		t.Errorf("resposta = %+v", got)
	}
	// O Jev respondeu (com baixa confiança) antes de escalar: a confiança e as
	// probabilidades dele devem acompanhar a resposta final mesmo vindo do
	// Claude, porque ele de fato opinou.
	if got.Confidence != 0.41 {
		t.Errorf("confidence = %v, queria 0.41 (a do jev)", got.Confidence)
	}
	if len(got.Probabilities) == 0 {
		t.Errorf("probabilities vazio, queria as do jev: %+v", got)
	}
}

func TestAnswerQuizMargemBaixaEscalaMesmoComConfiancaAlta(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevEmpatado, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if !got.Escalated {
		t.Error("0.88 vs 0.90 é empate técnico — tinha que escalar")
	}
}

func TestAnswerQuizExplainForcaEscalonamento(t *testing.T) {
	var chamadas []string
	req := reqExemplo()
	req.Explain = true
	got, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceClaude {
		t.Errorf("source = %q, queria claude (só ele explica)", got.Source)
	}
}

func TestAnswerQuizJevFalhaVaiDiretoNoFallback(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake("", errors.New("502"), fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceClaude || got.Answer != "A" {
		t.Errorf("resposta = %+v", got)
	}
	// O Jev nem chegou a opinar: confidence/probabilities são dele, e
	// publicá-los aqui seria inventar dado que não existe. 0/nil é o valor
	// deliberado, não um zero-value vazando por acidente.
	if got.Confidence != 0 {
		t.Errorf("confidence = %v, queria 0 (jev não respondeu)", got.Confidence)
	}
	if got.Probabilities != nil {
		t.Errorf("probabilities = %+v, queria nil (jev não respondeu)", got.Probabilities)
	}
}

func TestAnswerQuizFallbackFalhaDevolveJevDegradado(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevInseguro, nil, "", errors.New("502"), &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	// No meio de uma questão, um palpite de confiança 0.41 vale mais que uma
	// tela de erro.
	if got.Source != quizSourceJev || !got.Degraded || got.Answer != "B" {
		t.Errorf("resposta = %+v", got)
	}
	// Escalated fica false de propósito: o campo diz de onde veio a resposta
	// que está sendo lida, e esta veio do Jev (estágio não-escalado). Quem
	// sinaliza a tentativa frustrada de escalar é Degraded.
	if got.Escalated {
		t.Error("escalated deveria ficar false no caminho degradado (a resposta é a do jev)")
	}
}

func TestAnswerQuizAmbosFalham(t *testing.T) {
	var chamadas []string
	_, err := answerQuiz(context.Background(), reqExemplo(), depsFake("", errors.New("x"), "", errors.New("y"), &chamadas))
	if !errors.Is(err, errQuizUpstream) {
		t.Errorf("err = %v, queria errQuizUpstream", err)
	}
}

func TestAnswerQuizAceitaAlternativasJaSeparadas(t *testing.T) {
	var chamadas []string
	req := quizRequest{
		Question: "Qual a capital da Mongólia?",
		Options:  map[string]string{"A": "Astana", "B": "Ulan Bator", "C": "Bishkek"},
	}
	got, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Parsed.Question != req.Question || len(got.Parsed.Options) != 3 {
		t.Errorf("parsed = %+v", got.Parsed)
	}
	if len(got.Parsed.Order) != 3 || got.Parsed.Order[0] != "A" {
		t.Errorf("ordem = %v, queria rótulos ordenados (mapa em Go não tem ordem)", got.Parsed.Order)
	}
}

// TestAnswerQuizBlocoImpossivelDeSeparar cobria o comportamento ANTERIOR ao
// modo aberto: bloco sem alternativas reconhecíveis → 422 direto. Agora o
// bloco sem alternativas entra no modo aberto — "texto solto" (10
// caracteres não-espaço) segue dando erro, mas por ser curto demais pro
// modo aberto (errQuizTextoInsuficiente), não por ser "não separável". O
// caso "sem alternativa nenhuma, mas texto suficiente" tem teste dedicado
// (TestAnswerQuizSemAlternativasEntraNoModoAberto).
func TestAnswerQuizBlocoImpossivelDeSepararEntraNoModoAbertoMasTextoECurtoDemais(t *testing.T) {
	var chamadas []string
	_, err := answerQuiz(context.Background(), quizRequest{Raw: "texto solto"}, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if !errors.Is(err, errQuizTextoInsuficiente) {
		t.Errorf("err = %v, queria errQuizTextoInsuficiente", err)
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma (não gastar API com lixo)", chamadas)
	}
}

func TestAnswerQuizAlternativasJaSeparadasForaDoLimite(t *testing.T) {
	// resolveQuizParsed tem que respeitar os mesmos limites de
	// quizMinOptions/quizMaxOptions que parseQuizBlock aplica no caminho do
	// bloco cru — senão dá pra mandar 1 ou 15 alternativas pelo caminho
	// "já separado" sem nenhuma validação.
	var chamadas []string
	req := quizRequest{
		Question: "Pergunta com uma alternativa só?",
		Options:  map[string]string{"A": "única"},
	}
	_, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if !errors.Is(err, errQuizUnparseable) {
		t.Errorf("err = %v, queria errQuizUnparseable", err)
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma (não gastar API com entrada inválida)", chamadas)
	}
}

// ── imagem ────────────────────────────────────────────────────────────────

func reqExemploComImagem(mime string, tamanhoBytes int) quizRequest {
	req := reqExemplo()
	req.ImageBase64 = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", tamanhoBytes)))
	req.ImageMime = mime
	return req
}

func TestAnswerQuizComImagemNaoChamaJev(t *testing.T) {
	var chamadas []string
	req := reqExemploComImagem("image/png", 128)
	got, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	// O Jev não lê imagem: chamá-lo às cegas só gastaria tempo e dinheiro.
	for _, c := range chamadas {
		if c == "jev" {
			t.Errorf("chamadas = %v, não deveria chamar o jev quando há imagem", chamadas)
		}
	}
	if got.Source != quizSourceClaude || !got.Escalated {
		t.Errorf("source=%q escalated=%v, queria claude/true (visão só existe no fallback)", got.Source, got.Escalated)
	}
	if got.Answer != "A" {
		t.Errorf("answer = %q, queria %q", got.Answer, "A")
	}
	// Sem veredito do Jev, não existe confiança/probabilidades pra publicar.
	if got.Confidence != 0 {
		t.Errorf("confidence = %v, queria 0 (jev não opinou)", got.Confidence)
	}
	if got.Probabilities != nil {
		t.Errorf("probabilities = %+v, queria nil (jev não opinou)", got.Probabilities)
	}
}

func TestAnswerQuizComImagemEFallbackFalhaDevolveErro(t *testing.T) {
	var chamadas []string
	req := reqExemploComImagem("image/png", 128)
	_, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, "", errors.New("502"), &chamadas))
	// Sem imagem haveria o palpite do Jev pra degradar; com imagem não há o
	// que degradar (o Jev nem foi chamado), então isto tem que ser erro.
	if !errors.Is(err, errQuizUpstream) {
		t.Errorf("err = %v, queria errQuizUpstream (não há palpite do jev pra degradar)", err)
	}
	for _, c := range chamadas {
		if c == "jev" {
			t.Errorf("chamadas = %v, não deveria chamar o jev quando há imagem", chamadas)
		}
	}
}

func TestAnswerQuizSemImagemComportamentoIntacto(t *testing.T) {
	// Sanidade: sem imagem, o fluxo continua idêntico ao anterior — Jev
	// primeiro, sem qualquer menção a imagem na resposta.
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceJev || got.Escalated {
		t.Errorf("source=%q escalated=%v", got.Source, got.Escalated)
	}
	if len(chamadas) != 1 || chamadas[0] != "jev" {
		t.Errorf("chamadas = %v, queria só o jev", chamadas)
	}
}

func TestAnswerQuizImagemMimeInvalidoNaoChamaUpstream(t *testing.T) {
	casos := []struct {
		nome string
		mime string
	}{
		{"mime não suportado", "image/bmp"},
		{"mime de outro tipo de arquivo", "text/plain"},
		{"mime vazio com base64 presente", ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var chamadas []string
			req := reqExemploComImagem(c.mime, 128)
			_, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
			if !errors.Is(err, errQuizImagemMimeInvalido) {
				t.Errorf("err = %v, queria errQuizImagemMimeInvalido", err)
			}
			if len(chamadas) != 0 {
				t.Errorf("chamadas = %v, queria nenhuma (mime inválido não deve gastar upstream)", chamadas)
			}
		})
	}
}

// Base64 malformado com mime válido é um erro DIFERENTE de mime inválido —
// confundir as duas manda quem depura no cliente atrás do campo errado
// (achado da revisão: a versão anterior misturava as duas causas no mesmo
// sentinela errQuizImagemMimeInvalido).
func TestAnswerQuizImagemBase64InvalidoNaoChamaUpstream(t *testing.T) {
	var chamadas []string
	req := reqExemplo()
	req.ImageMime = "image/png"
	req.ImageBase64 = "não é base64 válido!!!"
	_, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if !errors.Is(err, errQuizImagemBase64Invalido) {
		t.Errorf("err = %v, queria errQuizImagemBase64Invalido", err)
	}
	if errors.Is(err, errQuizImagemMimeInvalido) {
		t.Error("base64 malformado não deveria virar erro de mime — mime aqui é válido")
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma (base64 inválido não deve gastar upstream)", chamadas)
	}
}

// ── modo aberto ──────────────────────────────────────────────────────────

const fbAbertoOK = `{"answer":"instruir o público sobre os comportamentos, procedimentos ou normas que devem ser seguidos","reasoning":"literal do enunciado"}`

func reqExemploAberto() quizRequest {
	return quizRequest{Raw: "Um cartaz instrucional é um material de função normativa porque tem como finalidade ___ o público sobre os ___ ou as ___ que devem ser seguidos"}
}

func TestAnswerQuizSemAlternativasEntraNoModoAberto(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemploAberto(), depsFake(jevConfiante, nil, fbAbertoOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Kind != quizKindAberta {
		t.Errorf("kind = %q, queria %q", got.Kind, quizKindAberta)
	}
	if got.Source != quizSourceClaude || !got.Escalated {
		t.Errorf("source=%q escalated=%v", got.Source, got.Escalated)
	}
	if got.Answer != "" {
		t.Errorf("answer = %q, queria vazio no modo aberto (não há rótulo)", got.Answer)
	}
	if got.AnswerText == "" {
		t.Error("answerText vazio no modo aberto")
	}
	if got.Confidence != 0 || got.Probabilities != nil {
		t.Errorf("confidence/probabilities deveriam ficar zerados no modo aberto: %+v", got)
	}
	// Prova por contador de chamadas, não só pela asserção de source: o Jev
	// só sabe escolher entre opções — sem opções não há o que escolher.
	for _, c := range chamadas {
		if c == "jev" {
			t.Errorf("chamadas = %v, o jev não deveria ser chamado no modo aberto", chamadas)
		}
	}
	if len(chamadas) != 1 || chamadas[0] != "fallback" {
		t.Errorf("chamadas = %v, queria só o fallback", chamadas)
	}
}

func TestAnswerQuizComAlternativasContinuaKindUnica(t *testing.T) {
	// Sanidade do contrato: o caminho de escolha única (comportamento
	// existente, intacto) preenche Kind com "unica" — renomeado de
	// "multipla", que agora nomeia o modo de múltipla resposta.
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Kind != quizKindUnica {
		t.Errorf("kind = %q, queria %q", got.Kind, quizKindUnica)
	}
	if len(got.Answers) != 0 || got.AnswerProbs != nil {
		t.Errorf("answers/answerProbs deveriam ficar ausentes no modo único: %+v", got)
	}
}

func TestAnswerQuizAbertoComImagemNaoChamaJev(t *testing.T) {
	var chamadas []string
	req := reqExemploAberto()
	req.ImageBase64 = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 128)))
	req.ImageMime = "image/png"
	got, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbAbertoOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Kind != quizKindAberta || got.Source != quizSourceClaude || !got.Escalated {
		t.Errorf("resposta = %+v", got)
	}
	if got.AnswerText == "" {
		t.Error("answerText vazio no modo aberto com imagem")
	}
	for _, c := range chamadas {
		if c == "jev" {
			t.Errorf("chamadas = %v, não deveria chamar o jev no modo aberto com imagem", chamadas)
		}
	}
}

func TestAnswerQuizAbertoTextoCurtoDemaisNaoChamaUpstream(t *testing.T) {
	var chamadas []string
	_, err := answerQuiz(context.Background(), quizRequest{Raw: "curto"}, depsFake(jevConfiante, nil, fbAbertoOK, nil, &chamadas))
	if !errors.Is(err, errQuizTextoInsuficiente) {
		t.Errorf("err = %v, queria errQuizTextoInsuficiente", err)
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma (texto curto não deve gastar upstream)", chamadas)
	}
}

func TestAnswerQuizAbertoTextoVazioNaoChamaUpstream(t *testing.T) {
	var chamadas []string
	_, err := answerQuiz(context.Background(), quizRequest{Raw: "   "}, depsFake(jevConfiante, nil, fbAbertoOK, nil, &chamadas))
	if !errors.Is(err, errQuizTextoInsuficiente) {
		t.Errorf("err = %v, queria errQuizTextoInsuficiente", err)
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma (texto vazio não deve gastar upstream)", chamadas)
	}
}

func TestAnswerQuizAbertoTextoLongoDemaisNaoChamaUpstream(t *testing.T) {
	var chamadas []string
	raw := strings.Repeat("a bcd ", quizOpenMaxChars) // bem acima do teto
	_, err := answerQuiz(context.Background(), quizRequest{Raw: raw}, depsFake(jevConfiante, nil, fbAbertoOK, nil, &chamadas))
	if !errors.Is(err, errQuizTextoInsuficiente) {
		t.Errorf("err = %v, queria errQuizTextoInsuficiente", err)
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma (texto longo demais não deve gastar upstream)", chamadas)
	}
}

func TestAnswerQuizOptionsForaDoLimiteNaoEntraNoModoAberto(t *testing.T) {
	// options mal-formado (fora do limite de 2–9) é um erro diferente de
	// "sem alternativa nenhuma": o cliente já tentou separar e errou o
	// formato — isso continua 422 UNPARSEABLE, não cai pro modo aberto.
	var chamadas []string
	req := quizRequest{Question: "Pergunta com uma alternativa só?", Options: map[string]string{"A": "única"}}
	got, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbAbertoOK, nil, &chamadas))
	if !errors.Is(err, errQuizUnparseable) {
		t.Errorf("err = %v, queria errQuizUnparseable", err)
	}
	if got.Kind == quizKindAberta {
		t.Error("não deveria ter entrado no modo aberto com options fora do limite")
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma", chamadas)
	}
}

func TestAnswerQuizAbertoFallbackFalhaDevolveErro(t *testing.T) {
	var chamadas []string
	_, err := answerQuiz(context.Background(), reqExemploAberto(), depsFake(jevConfiante, nil, "", errors.New("502"), &chamadas))
	// Sem Jev no modo aberto, não há palpite nenhum pra degradar.
	if !errors.Is(err, errQuizUpstream) {
		t.Errorf("err = %v, queria errQuizUpstream (não há palpite pra degradar no modo aberto)", err)
	}
	for _, c := range chamadas {
		if c == "jev" {
			t.Errorf("chamadas = %v, não deveria chamar o jev no modo aberto", chamadas)
		}
	}
}

// ── modo múltipla resposta ("marque todas que se aplicam") ────────────────

// depsFakeMulti é depsFake com minMultiAlt preenchido (QUIZ_MULTI_MIN,
// default 0.45) — as demais funções fake continuam devolvendo jevBody/fbText
// crus, ignorando o corpo da chamada.
func depsFakeMulti(jevBody string, jevErr error, fbText string, fbErr error, chamadas *[]string) quizDeps {
	d := depsFake(jevBody, jevErr, fbText, fbErr, chamadas)
	d.minMultiAlt = 0.45
	return d
}

func reqExemploMultipla() quizRequest {
	return quizRequest{Raw: "Assinale as alternativas corretas sobre o tema.\nA) primeira\nB) segunda\nC) terceira\nD) quarta"}
}

// jevMultiplaAltaSemZonaCinzenta: números medidos em produção contra uma
// questão real de múltipla resposta (gabarito A, C, D) — ver a spec da
// Task. Nenhuma alternativa fica entre QUIZ_MULTI_MIN (0.45) e 0.65
// (quizMultiploAltGrayHigh), logo não escala.
const jevMultiplaAltaSemZonaCinzenta = `{"answers":{
	"resposta":{"type":"choice","choice":"A","confidence":0.5,"probabilities":{"A":0.5,"B":0.1,"C":0.2,"D":0.2}},
	"multipla":{"type":"noul","noul":0.91},
	"alt_A":{"type":"noul","noul":0.86},
	"alt_B":{"type":"noul","noul":0.03},
	"alt_C":{"type":"noul","noul":0.87},
	"alt_D":{"type":"noul","noul":0.82}
}}`

func TestAnswerQuizMultiplaAltaMarcaAsAcimaDoLimiarNaOrdem(t *testing.T) {
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemploMultipla(), depsFakeMulti(jevMultiplaAltaSemZonaCinzenta, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Kind != quizKindMultipla {
		t.Errorf("kind = %q, queria %q", got.Kind, quizKindMultipla)
	}
	if want := []string{"A", "C", "D"}; !slicesEqual(got.Answers, want) {
		t.Errorf("answers = %v, queria %v (na ordem de p.Order)", got.Answers, want)
	}
	if len(got.AnswerProbs) != 4 {
		t.Errorf("answerProbs = %v, queria as 4 alternativas (não só as marcadas)", got.AnswerProbs)
	}
	if got.Source != quizSourceJev || got.Escalated {
		t.Errorf("source=%q escalated=%v, queria jev/false (nada na zona cinzenta)", got.Source, got.Escalated)
	}
	if got.Answer != "" || got.AnswerText != "" {
		t.Errorf("answer/answerText deveriam ficar vazios no modo múltipla: %+v", got)
	}
	// Escalar à toa custa dinheiro e tempo: nada na zona cinzenta, então só o
	// jev deveria ser chamado.
	if len(chamadas) != 1 || chamadas[0] != "jev" {
		t.Errorf("chamadas = %v, queria só o jev", chamadas)
	}
}

func TestAnswerQuizMultiplaBaixaSegueCaminhoDeHoje(t *testing.T) {
	jevBody := `{"answers":{
		"resposta":{"type":"choice","choice":"B","confidence":0.93,"probabilities":{"A":0.02,"B":0.93,"C":0.05}},
		"multipla":{"type":"noul","noul":0.05}
	}}`
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFakeMulti(jevBody, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Kind != quizKindUnica {
		t.Errorf("kind = %q, queria %q (multipla=0.05, bem abaixo do limiar)", got.Kind, quizKindUnica)
	}
	if len(got.Answers) != 0 || got.AnswerProbs != nil {
		t.Errorf("answers/answerProbs deveriam ficar ausentes: %+v", got)
	}
}

func TestAnswerQuizMultiplaNenhumaAlternativaAcimaDoLimiarCaiParaAMaior(t *testing.T) {
	jevBody := `{"answers":{
		"resposta":{"type":"choice","choice":"A","confidence":0.5,"probabilities":{"A":0.3,"B":0.2,"C":0.25,"D":0.1}},
		"multipla":{"type":"noul","noul":0.70},
		"alt_A":{"type":"noul","noul":0.30},
		"alt_B":{"type":"noul","noul":0.20},
		"alt_C":{"type":"noul","noul":0.25},
		"alt_D":{"type":"noul","noul":0.10}
	}}`
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemploMultipla(), depsFakeMulti(jevBody, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	// Nenhuma das quatro passa de QUIZ_MULTI_MIN (0.45) — nunca devolve
	// lista vazia numa questão de múltipla resposta: cai pra maior (A, 0.30).
	if want := []string{"A"}; !slicesEqual(got.Answers, want) {
		t.Errorf("answers = %v, queria %v (a de maior probabilidade)", got.Answers, want)
	}
	if got.Escalated {
		t.Error("nada na zona cinzenta (todas abaixo de 0.45) — não deveria escalar")
	}
}

func TestAnswerQuizMultiplaZonaCinzentaEscalaParaClaude(t *testing.T) {
	jevBody := `{"answers":{
		"resposta":{"type":"choice","choice":"A","confidence":0.5,"probabilities":{"A":0.5,"B":0.3,"C":0.2}},
		"multipla":{"type":"noul","noul":0.70},
		"alt_A":{"type":"noul","noul":0.86},
		"alt_B":{"type":"noul","noul":0.50},
		"alt_C":{"type":"noul","noul":0.10}
	}}`
	fbMultiplaOK := `{"answers":["A","C"],"reasoning":"porque sim"}`
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFakeMulti(jevBody, nil, fbMultiplaOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	// alt_B (0.50) está entre QUIZ_MULTI_MIN (0.45) e 0.65 — zona cinzenta de
	// UMA alternativa específica, questão já classificada múltipla (0.70).
	if !got.Escalated || got.Source != quizSourceClaude {
		t.Errorf("source=%q escalated=%v, queria claude/true (alt_B na zona cinzenta)", got.Source, got.Escalated)
	}
	if want := []string{"A", "C"}; !slicesEqual(got.Answers, want) {
		t.Errorf("answers = %v, queria %v (resposta do claude)", got.Answers, want)
	}
	if len(got.AnswerProbs) != 3 {
		t.Errorf("answerProbs = %v, queria as probabilidades do jev mesmo escalado", got.AnswerProbs)
	}
	if len(chamadas) != 2 || chamadas[0] != "jev" || chamadas[1] != "fallback" {
		t.Errorf("chamadas = %v, queria jev depois fallback", chamadas)
	}
}

func TestAnswerQuizMultiplaAmbiguaEscalaParaClaude(t *testing.T) {
	// multipla em 0.50 — nem uma coisa nem outra (faixa 0.40–0.60): incerto
	// demais pro Jev decidir sozinho se é única ou múltipla resposta.
	jevBody := `{"answers":{
		"resposta":{"type":"choice","choice":"A","confidence":0.9,"probabilities":{"A":0.9,"B":0.05,"C":0.05}},
		"multipla":{"type":"noul","noul":0.50},
		"alt_A":{"type":"noul","noul":0.90},
		"alt_B":{"type":"noul","noul":0.05},
		"alt_C":{"type":"noul","noul":0.05}
	}}`
	fbMultiplaOK := `{"answers":["A"],"reasoning":"só a A está certa"}`
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFakeMulti(jevBody, nil, fbMultiplaOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Kind != quizKindMultipla {
		t.Errorf("kind = %q, queria %q (mesmo ambígua, a resposta escalada vira lista)", got.Kind, quizKindMultipla)
	}
	if !got.Escalated || got.Source != quizSourceClaude {
		t.Errorf("source=%q escalated=%v, queria claude/true", got.Source, got.Escalated)
	}
	if want := []string{"A"}; !slicesEqual(got.Answers, want) {
		t.Errorf("answers = %v, queria %v", got.Answers, want)
	}
}

func TestAnswerQuizMultiplaFallbackFalhaDevolveJevDegradado(t *testing.T) {
	// Precisa de uma escalada de verdade (zona cinzenta em alt_B) pra
	// exercitar o caminho de degradação — sem escalada não há fallback pra
	// falhar.
	jevBody := `{"answers":{
		"resposta":{"type":"choice","choice":"A","confidence":0.5,"probabilities":{"A":0.5,"B":0.3,"C":0.2}},
		"multipla":{"type":"noul","noul":0.70},
		"alt_A":{"type":"noul","noul":0.86},
		"alt_B":{"type":"noul","noul":0.50},
		"alt_C":{"type":"noul","noul":0.10}
	}}`
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFakeMulti(jevBody, nil, "", errors.New("502"), &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Source != quizSourceJev || !got.Degraded {
		t.Errorf("source=%q degraded=%v, queria jev/true (fallback falhou, mas o jev tem palpite)", got.Source, got.Degraded)
	}
	if got.Escalated {
		t.Error("escalated deveria ficar false no caminho degradado (a resposta é a do jev)")
	}
	// A) 0.86 e nada mais acima de 0.45 (B fica em 0.50... na verdade B
	// também passa do limiar 0.45) — confere que a seleção por limiar ainda
	// roda no degradado.
	if len(got.Answers) == 0 {
		t.Error("answers vazio no caminho degradado — nunca deveria devolver lista vazia")
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAnswerQuizImagemAcimaDoLimiteNaoChamaUpstream(t *testing.T) {
	var chamadas []string
	req := reqExemploComImagem("image/png", quizMaxImageBytes+1)
	_, err := answerQuiz(context.Background(), req, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if !errors.Is(err, errQuizImagemGrandeDemais) {
		t.Errorf("err = %v, queria errQuizImagemGrandeDemais", err)
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma (imagem grande demais não deve gastar upstream)", chamadas)
	}
}
