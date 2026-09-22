package main

import (
	"context"
	"errors"
	"testing"
)

func depsFake(jevBody string, jevErr error, fbBody string, fbErr error, chamadas *[]string) quizDeps {
	return quizDeps{
		jev: func(ctx context.Context, body []byte) ([]byte, error) {
			*chamadas = append(*chamadas, "jev")
			if jevErr != nil {
				return nil, jevErr
			}
			return []byte(jevBody), nil
		},
		fallback: func(ctx context.Context, prompt string) ([]byte, error) {
			*chamadas = append(*chamadas, "fallback")
			if fbErr != nil {
				return nil, fbErr
			}
			return []byte(fbBody), nil
		},
		fallbackAdapter: chatAdapterAnthropic,
		minConfidence:   0.75,
		minMargin:       0.15,
	}
}

const (
	jevConfiante = `{"answers":{"resposta":{"type":"choice","choice":"B","confidence":0.93,"probabilities":{"A":0.02,"B":0.93,"C":0.05}}}}`
	jevInseguro  = `{"answers":{"resposta":{"type":"choice","choice":"B","confidence":0.41,"probabilities":{"A":0.39,"B":0.41,"C":0.20}}}}`
	jevEmpatado  = `{"answers":{"resposta":{"type":"choice","choice":"B","confidence":0.90,"probabilities":{"A":0.88,"B":0.90}}}}`
	fbOK         = `{"content":[{"type":"text","text":"{\"answer\":\"A\",\"reasoning\":\"porque sim\"}"}]}`
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

func TestAnswerQuizBlocoImpossivelDeSeparar(t *testing.T) {
	var chamadas []string
	_, err := answerQuiz(context.Background(), quizRequest{Raw: "texto solto"}, depsFake(jevConfiante, nil, fbOK, nil, &chamadas))
	if !errors.Is(err, errQuizUnparseable) {
		t.Errorf("err = %v, queria errQuizUnparseable", err)
	}
	if len(chamadas) != 0 {
		t.Errorf("chamadas = %v, queria nenhuma (não gastar API com lixo)", chamadas)
	}
}
