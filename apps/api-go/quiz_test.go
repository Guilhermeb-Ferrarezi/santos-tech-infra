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

func TestAnswerQuizComAlternativasContinuaKindMultipla(t *testing.T) {
	// Sanidade do contrato: o caminho de múltipla escolha (comportamento
	// existente, intacto) agora também preenche Kind.
	var chamadas []string
	got, err := answerQuiz(context.Background(), reqExemplo(), depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if err != nil {
		t.Fatalf("answerQuiz: %v", err)
	}
	if got.Kind != quizKindMultipla {
		t.Errorf("kind = %q, queria %q", got.Kind, quizKindMultipla)
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
