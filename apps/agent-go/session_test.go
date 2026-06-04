package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func testManager() *SessionManager {
	s := &Server{cfg: Config{WorkspaceRoot: "/tmp/agent-test", ClaudeBin: "claude", DefaultModel: "sonnet"}}
	return newSessionManager(s)
}

func TestClaudeArgsFirstTurnUsesSessionID(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "sess-1", Model: "opus", Workdir: "/tmp/agent-test/c1", SessionStarted: false}
	args := m.claudeArgs(conv)

	if !slices.Contains(args, "--session-id") {
		t.Fatalf("primeiro turno deveria usar --session-id: %v", args)
	}
	if slices.Contains(args, "--resume") {
		t.Fatalf("primeiro turno não deveria usar --resume: %v", args)
	}
	assertPairValue(t, args, "--session-id", "sess-1")
	assertPairValue(t, args, "--model", "opus")
	assertPairValue(t, args, "--add-dir", "/tmp/agent-test/c1")
}

func TestClaudeArgsResumesStartedSession(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "sess-1", Model: "sonnet", Workdir: "/tmp/agent-test/c1", SessionStarted: true}
	args := m.claudeArgs(conv)

	if !slices.Contains(args, "--resume") {
		t.Fatalf("turno seguinte deveria usar --resume: %v", args)
	}
	if slices.Contains(args, "--session-id") {
		t.Fatalf("turno seguinte não deveria usar --session-id: %v", args)
	}
	assertPairValue(t, args, "--resume", "sess-1")
}

func TestClaudeArgsToolsDisabledDropsToolAccess(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: "/tmp/agent-test/c1", ToolsDisabled: true}
	args := m.claudeArgs(conv)

	if slices.Contains(args, "--add-dir") {
		t.Fatalf("tools_disabled não deveria passar --add-dir: %v", args)
	}
	if !slices.Contains(args, "--disallowed-tools") {
		t.Fatalf("tools_disabled deveria passar --disallowed-tools: %v", args)
	}
	// a string de tools deve conter as embutidas
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "Bash") || !strings.Contains(joined, "Write") {
		t.Fatalf("--disallowed-tools deveria cobrir Bash/Write: %v", args)
	}
}

func TestClaudeArgsToolsEnabledKeepsAddDir(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: "/tmp/agent-test/c1", ToolsDisabled: false}
	args := m.claudeArgs(conv)
	if !slices.Contains(args, "--add-dir") {
		t.Fatalf("tools habilitadas deveria manter --add-dir: %v", args)
	}
	if slices.Contains(args, "--disallowed-tools") {
		t.Fatalf("tools habilitadas não deveria passar --disallowed-tools: %v", args)
	}
}

func TestDeltaTextExtractsTextDelta(t *testing.T) {
	var ev map[string]any
	_ = json.Unmarshal([]byte(`{
	  "type":"stream_event",
	  "event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"olá"}}
	}`), &ev)
	if got := deltaText(ev); got != "olá" {
		t.Fatalf("esperava 'olá', veio %q", got)
	}
}

func TestDeltaTextIgnoresNonText(t *testing.T) {
	var ev map[string]any
	_ = json.Unmarshal([]byte(`{"type":"stream_event","event":{"type":"message_start"}}`), &ev)
	if got := deltaText(ev); got != "" {
		t.Fatalf("esperava vazio, veio %q", got)
	}
}

func TestMessageContentParsesBlocks(t *testing.T) {
	var ev map[string]any
	_ = json.Unmarshal([]byte(`{
	  "type":"assistant",
	  "message":{"content":[
	    {"type":"text","text":"oi"},
	    {"type":"tool_use","name":"Bash","input":{"command":"ls"}}
	  ]}
	}`), &ev)
	blocks := messageContent(ev)
	if len(blocks) != 2 {
		t.Fatalf("esperava 2 blocos, veio %d", len(blocks))
	}
	if blocks[0]["type"] != "text" || blocks[1]["name"] != "Bash" {
		t.Fatalf("blocos inesperados: %v", blocks)
	}
}

func assertPairValue(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i, a := range args {
		if a == flag {
			if i+1 >= len(args) || args[i+1] != want {
				t.Fatalf("flag %s: esperava %q, veio %q", flag, want, valueAt(args, i+1))
			}
			return
		}
	}
	t.Fatalf("flag %s ausente em %v", flag, args)
}

func valueAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return "<fim>"
}
