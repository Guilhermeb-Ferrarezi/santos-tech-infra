package main

import "testing"

func TestActiveSyncHits(t *testing.T) {
	mk := func(active bool) Hit {
		return Hit{Keyword: "OPENAI_API_KEY", RepoFullName: "a/b", MatchedValue: "sk-x", LiveChecked: true, LiveActive: active}
	}

	out := activeSyncHits([]Hit{mk(true), mk(false), mk(true)}, 10)
	if len(out) != 2 {
		t.Fatalf("queria 2 hits ativos, veio %d", len(out))
	}

	// cap respeitado
	out2 := activeSyncHits([]Hit{mk(true), mk(true), mk(true)}, 2)
	if len(out2) != 2 {
		t.Fatalf("cap 2 não respeitado: %d", len(out2))
	}

	// lista vazia não quebra
	if got := activeSyncHits(nil, 10); len(got) != 0 {
		t.Fatalf("nil deveria devolver lista vazia: %d", len(got))
	}
}
