package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Testes dos handlers do gerador de currículo, fase 2 — só os caminhos que
// retornam ANTES de tocar o banco (mesmo padrão de handlers_posaula_test.go).

func curriculoReq(body string, userID int64) *http.Request {
	r := httptest.NewRequest("POST", "/portal/curriculo/reescrever", strings.NewReader(body))
	return reqAs(r, userID)
}

func TestCurriculoPostReescreverValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})
	cases := []struct {
		name string
		body string
	}{
		{"corpo inválido", "xxx"},
		{"campo desconhecido", `{"campo":"nome","texto":"x","maxChars":100}`},
		{"sem campo", `{"texto":"x","maxChars":100}`},
		{"texto vazio", `{"campo":"resumo","texto":"   ","maxChars":100}`},
		{"texto acima do teto", `{"campo":"resumo","texto":"` + strings.Repeat("a", curriculoTextoOriginalMax+1) + `","maxChars":100}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.handleCurriculoPostReescrever(w, curriculoReq(tc.body, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
}

// "objetivo" é o único campo que pode ser pedido com texto vazio — vira
// GERAÇÃO do zero, pensando em quem nunca trabalhou. Sem fila, a validação
// passa e o handler falha depois, no enqueue (503) — prova que não foi a
// validação que rejeitou.
func TestCurriculoPostReescreverObjetivoVazioPassaDaValidacao(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleCurriculoPostReescrever(w, curriculoReq(`{"campo":"objetivo","texto":"","maxChars":160}`, 1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want %d (deveria passar da validação e falhar só no enqueue) body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

func TestCurriculoGetReescreverIDInvalido(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("GET", "/portal/curriculo/reescrever/x", nil)
	r.SetPathValue("id", "x")
	r = reqAs(r, 1)
	w := httptest.NewRecorder()
	s.handleCurriculoGetReescrever(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

// Sem asynq (s.queue == nil) o enqueue avisa em vez de tocar o banco.
func TestEnqueueCurriculoReescreverSemFila(t *testing.T) {
	s := testServer(Config{})
	_, err := s.enqueueCurriculoReescrever(context.Background(), 1, curriculoEnqueueInput{
		Campo: "resumo", Texto: "x", MaxChars: 460,
	})
	if !errors.Is(err, errFilaIndisponivel) {
		t.Errorf("err=%v want errFilaIndisponivel", err)
	}
}
