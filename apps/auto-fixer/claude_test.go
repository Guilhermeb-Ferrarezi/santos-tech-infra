package main

import "testing"

func TestParseClaudeResult(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"olhando"}]}}`,
		`{"type":"result","subtype":"success","result":"Corrigi o Dockerfile: faltava COPY do go.sum."}`,
	}
	if got := parseClaudeResult(lines); got != "Corrigi o Dockerfile: faltava COPY do go.sum." {
		t.Fatalf("got %q", got)
	}
}

func TestParseClaudeResultEmpty(t *testing.T) {
	if got := parseClaudeResult([]string{`{"type":"system"}`}); got != "" {
		t.Fatalf("got %q", got)
	}
}
