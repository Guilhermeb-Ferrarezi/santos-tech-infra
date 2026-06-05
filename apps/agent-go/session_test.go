package main

import (
	"encoding/json"
	"slices"
	"testing"
)

func testManager() *SessionManager {
	s := &Server{cfg: Config{WorkspaceRoot: "/tmp/agent-test", ClaudeBin: "claude", DefaultModel: "sonnet"}}
	return newSessionManager(s)
}

func TestClaudeArgsFirstTurnUsesSessionID(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "sess-1", Model: "opus", Workdir: "/tmp/agent-test/c1", SessionStarted: false}
	args := m.claudeArgs(conv, "")

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
	args := m.claudeArgs(conv, "")

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
	args := m.claudeArgs(conv, "")

	if slices.Contains(args, "--add-dir") {
		t.Fatalf("tools_disabled não deveria passar --add-dir: %v", args)
	}
	// Gate por allow-list vazia (não deny-list): --allowed-tools "" nega tudo por padrão.
	if !slices.Contains(args, "--allowed-tools") {
		t.Fatalf("tools_disabled deveria passar --allowed-tools: %v", args)
	}
	assertPairValue(t, args, "--allowed-tools", "")
	if slices.Contains(args, "--disallowed-tools") {
		t.Fatalf("tools_disabled não deveria mais usar deny-list (--disallowed-tools): %v", args)
	}
	// allow-list vazia só gateia se NÃO houver --dangerously-skip-permissions (que a ignora).
	if slices.Contains(args, "--dangerously-skip-permissions") {
		t.Fatalf("tools_disabled não deveria passar --dangerously-skip-permissions: %v", args)
	}
}

func TestClaudeArgsToolsEnabledKeepsAddDir(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: "/tmp/agent-test/c1", ToolsDisabled: false}
	args := m.claudeArgs(conv, "")
	if !slices.Contains(args, "--add-dir") {
		t.Fatalf("tools habilitadas deveria manter --add-dir: %v", args)
	}
	if slices.Contains(args, "--disallowed-tools") {
		t.Fatalf("tools habilitadas não deveria passar --disallowed-tools: %v", args)
	}
}

func TestClaudeArgsMediaScopedRead(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: "/ws/abc", ToolsDisabled: true}

	// Sem anexos: allow-list vazia (caminho atual intocado).
	args := m.claudeArgs(conv, "")
	assertPairValue(t, args, "--allowed-tools", "")

	// Com anexos: Read escopado APENAS ao dir de mídia da conversa.
	args = m.claudeArgs(conv, "/ws/abc/media/**")
	assertPairValue(t, args, "--allowed-tools", "Read(/ws/abc/media/**)")

	// WebSearch ligado compõe com o Read.
	conv.WebSearch = true
	args = m.claudeArgs(conv, "/ws/abc/media/**")
	assertPairValue(t, args, "--allowed-tools", "WebSearch,Read(/ws/abc/media/**)")
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

func TestClaudeArgsEffortPassesFlag(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: "/tmp/agent-test/c1", Effort: "high"}
	args := m.claudeArgs(conv, "")
	assertPairValue(t, args, "--effort", "high")
}

func TestClaudeArgsEffortVazioOmiteFlag(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: "/tmp/agent-test/c1"}
	if slices.Contains(m.claudeArgs(conv, ""), "--effort") {
		t.Fatalf("sem effort não deveria passar --effort")
	}
}

func TestClaudeArgsToolsDisabledComWebSearchLiberaSoBusca(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: "/tmp/agent-test/c1", ToolsDisabled: true, WebSearch: true}
	args := m.claudeArgs(conv, "")
	assertPairValue(t, args, "--allowed-tools", "WebSearch")
	if slices.Contains(args, "--add-dir") || slices.Contains(args, "--dangerously-skip-permissions") {
		t.Fatalf("web search não deveria reabrir add-dir/skip-permissions: %v", args)
	}
}

func TestClaudeArgsToolsDisabledSemWebSearchSegueVazio(t *testing.T) {
	m := testManager()
	conv := &Conversation{ID: "c1", SessionID: "s1", Model: "sonnet", Workdir: "/tmp/agent-test/c1", ToolsDisabled: true}
	assertPairValue(t, m.claudeArgs(conv, ""), "--allowed-tools", "")
}
