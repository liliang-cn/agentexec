package cliagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func newClaudeAAS() Session {
	return NewClaude(
		WithName("claude-code"),
		WithModelEnv("CLAUDE_MODEL"),
		WithMCPConfig(".aas-mcp.json", true),
		WithAllowedModes([]string{"headless-code", "terminal-task"}),
	).NewSession()
}

func TestClaudeMeta(t *testing.T) {
	p := NewClaude(WithName("claude-code"))
	if p.Name() != "claude-code" {
		t.Fatalf("name=%q", p.Name())
	}
	c := p.Capabilities()
	if !c.Streaming || !c.Resume || !c.Plugins || !c.MCP || !c.SupportsPTY || !c.RequiresWorkspace {
		t.Fatalf("caps=%+v", c)
	}
}

func TestClaudeGoldenArgv(t *testing.T) {
	spec, err := newClaudeAAS().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "do it", WorkspacePath: "/work",
		PermissionMode: PermissionBypass,
		Env:            map[string]string{"CLAUDE_MODEL": "opus"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claude", "--print", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "bypassPermissions", "--model", "opus", "do it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=\n%v\nwant\n%v", spec.Argv, want)
	}
	if spec.WorkDir != "/work" {
		t.Fatalf("workdir=%q", spec.WorkDir)
	}
}

func TestClaudeUnsupportedMode(t *testing.T) {
	_, err := newClaudeAAS().BuildCommand(context.Background(), Request{Mode: "nope", Prompt: "x"})
	if err == nil {
		t.Fatal("expected ErrUnsupportedMode")
	}
}

func TestClaudeResumeWinsOverContinue(t *testing.T) {
	spec, _ := newClaudeAAS().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "p", ResumeSessionID: "s1", Continue: true,
	})
	if !slices.Contains(spec.Argv, "--resume") || slices.Contains(spec.Argv, "--continue") {
		t.Fatalf("argv=%v", spec.Argv)
	}
}

func TestClaudeMCPMergeWritesConfigAndStrict(t *testing.T) {
	ws := t.TempDir()
	plug := t.TempDir()
	writeMCP(t, plug, `{"mcpServers":{"p":{"command":"pc"}}}`)
	spec, err := newClaudeAAS().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "p", WorkspacePath: ws,
		ExtraMCPServers: map[string]any{"aas": map[string]any{"command": "exe"}},
		Plugins:         []PluginRef{{Name: "p", Path: plug}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.Argv, "--mcp-config") || !slices.Contains(spec.Argv, "--strict-mcp-config") {
		t.Fatalf("argv=%v", spec.Argv)
	}
	if !slices.Contains(spec.Argv, "--plugin-dir") {
		t.Fatalf("argv missing --plugin-dir: %v", spec.Argv)
	}
	data, err := os.ReadFile(filepath.Join(ws, ".aas-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"aas"`) || !contains(string(data), `"p"`) {
		t.Fatalf("merged config = %s", data)
	}
}

func TestClaudeEnvDropsEmptyBaseKeepsTimeout(t *testing.T) {
	s := NewClaude(WithName("claude-code"), WithBaseEnv(map[string]string{
		"MCP_TOOL_TIMEOUT": "1800000", "EMPTY": "",
	}), WithAllowedModes([]string{"headless-code"})).NewSession()
	spec, _ := s.BuildCommand(context.Background(), Request{Mode: "headless-code", Prompt: "p"})
	if !slices.Contains(spec.Env, "MCP_TOOL_TIMEOUT=1800000") {
		t.Fatalf("env=%v", spec.Env)
	}
	for _, e := range spec.Env {
		if e == "EMPTY=" {
			t.Fatalf("empty base value should be dropped: %v", spec.Env)
		}
	}
}

func TestClaudeParseAssistantTextAndToolCall(t *testing.T) {
	s := newClaudeAAS()
	line := `{"type":"assistant","session_id":"s9","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}},{"type":"text","text":"hi"}]}}` + "\n"
	ev, _ := s.ParseChunk([]byte(line))
	if findEvent(ev, EventToolCall) == nil {
		t.Fatalf("no tool_call: %v", ev)
	}
	msg := findEvent(ev, EventAgentMessage)
	if msg == nil || msg.Payload["text"] != "hi" {
		t.Fatalf("msg=%v", msg)
	}
	if s.SessionID() != "s9" {
		t.Fatalf("sid=%q", s.SessionID())
	}
}

func TestClaudeSystemFrameCarriesRaw(t *testing.T) {
	s := newClaudeAAS()
	ev, _ := s.ParseChunk([]byte(`{"type":"system","subtype":"init","session_id":"s1","model":"opus"}` + "\n"))
	m := findEvent(ev, EventAgentMessage)
	if m == nil || m.Payload["role"] != "system" || m.Payload["raw"] == nil {
		t.Fatalf("system event=%v", m)
	}
}

func TestClaudeUsageResultCanonical(t *testing.T) {
	s := newClaudeAAS()
	s.ParseChunk([]byte(`{"type":"assistant","message":{"usage":{"input_tokens":3,"output_tokens":1}}}` + "\n"))
	res, _, _ := s.Finalize(context.Background(),
		[]byte(`{"type":"result","result":"done","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2},"total_cost_usd":0.05}`+"\n"), 0)
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 4 || res.Usage.CacheTokens != 2 || res.Usage.EstimatedCostUSD != 0.05 {
		t.Fatalf("usage=%+v", res.Usage)
	}
}

func TestClaudeUsageFallbackWithoutResult(t *testing.T) {
	s := newClaudeAAS()
	s.ParseChunk([]byte(`{"type":"assistant","message":{"usage":{"input_tokens":3,"output_tokens":1}}}` + "\n"))
	res, _, _ := s.Finalize(context.Background(), nil, 0)
	if res.Usage.InputTokens != 3 || res.Usage.OutputTokens != 1 {
		t.Fatalf("fallback usage=%+v", res.Usage)
	}
}
