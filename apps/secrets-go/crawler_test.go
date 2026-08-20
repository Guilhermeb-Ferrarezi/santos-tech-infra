package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDateSplitQueries_CoversFrom2015ToCurrentYear(t *testing.T) {
	queries := dateSplitQueries("OPENAI_API_KEY", 2)
	if len(queries) == 0 {
		t.Fatalf("esperava pelo menos uma janela")
	}
	first := queries[0]
	if !strings.HasPrefix(first, "OPENAI_API_KEY created:2015-01-01..") {
		t.Errorf("primeira janela deveria começar em 2015, veio %q", first)
	}
	last := queries[len(queries)-1]
	wantEnd := fmt.Sprintf("%d-12-31", time.Now().Year())
	if !strings.HasSuffix(last, wantEnd) {
		t.Errorf("última janela deveria terminar em %s, veio %q", wantEnd, last)
	}
}

func TestDateSplitQueries_WindowsAreContiguousAndNonOverlapping(t *testing.T) {
	queries := dateSplitQueries("AKIA", 3)
	if len(queries) == 0 {
		t.Fatalf("esperava janelas")
	}

	// extrai (anoInicio, anoFim) de cada "AKIA created:AAAA-01-01..BBBB-12-31"
	parse := func(q string) (int, int) {
		rangePart := strings.SplitN(q, " ", 2)[1]
		from, to, ok := strings.Cut(rangePart, "..")
		if !ok {
			t.Fatalf("query malformada: %q", q)
		}
		var f, e int
		fmt.Sscanf(from, "created:%d-01-01", &f)
		fmt.Sscanf(to, "%d-12-31", &e)
		return f, e
	}

	for i, q := range queries {
		from, to := parse(q)
		if to < from {
			t.Fatalf("janela %d invertida (%d > %d): %q", i, from, to, q)
		}
		if i > 0 {
			_, prevTo := parse(queries[i-1])
			if from != prevTo+1 {
				t.Errorf("janela %d deveria começar no ano seguinte à anterior (%d+1), veio %d: %q", i, prevTo, from, q)
			}
		}
	}
}
