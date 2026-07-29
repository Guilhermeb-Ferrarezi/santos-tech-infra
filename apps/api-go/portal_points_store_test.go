package main

import (
	"context"
	"testing"
)

func TestGrantPointsForAnswersEmptyIsNoop(t *testing.T) {
	// Sem portalDB configurado (nil) — não deve tentar consultar o banco pra uma
	// lista vazia de respostas, senão panica com nil pointer.
	s := testServer(Config{})
	s.grantPointsForAnswers(context.Background(), nil)
	s.grantPointsForAnswers(context.Background(), []int64{})
}

func TestRoundPercent(t *testing.T) {
	cases := map[float64]float64{
		0:                    0,
		100:                  100,
		50:                   50,
		float64(2) / 3 * 100: 66.67, // 66.666...
		float64(1) / 3 * 100: 33.33, // 33.333...
		12.344:               12.34,
	}
	for in, want := range cases {
		if got := roundPercent(in); got != want {
			t.Errorf("roundPercent(%v) = %v, quer %v", in, got, want)
		}
	}
}
