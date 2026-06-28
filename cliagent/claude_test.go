package cliagent

import (
	"context"
	"slices"
	"testing"
)

func TestClaudeProviderMeta(t *testing.T) {
	p := NewClaude()
	if p.Name() != "claude" {
		t.Fatalf("Name = %q, want claude", p.Name())
	}
	caps := p.Capabilities()
	if !caps.Streaming || !caps.Resume || !caps.Plugins || !caps.MCP {
		t.Fatalf("claude should support all caps, got %+v", caps)
	}
}

func TestClaudeBuildCommandBasic(t *testing.T) {
	s := NewClaude().NewSession()
	spec, err := s.BuildCommand(context.Background(), Request{
		Prompt:        "hello",
		WorkspacePath: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("Argv = %v, want %v", spec.Argv, want)
	}
	if string(spec.Stdin) != "hello" {
		t.Fatalf("Stdin = %q, want hello", spec.Stdin)
	}
	if spec.WorkDir != "/work" {
		t.Fatalf("WorkDir = %q, want /work", spec.WorkDir)
	}
}

func TestClaudeBuildCommandFlags(t *testing.T) {
	s := NewClaude().NewSession()
	spec, err := s.BuildCommand(context.Background(), Request{
		Prompt:          "hi",
		Model:           "claude-opus-4-8",
		SystemPrompt:    "be terse",
		Continue:        true,
		ResumeSessionID: "sess-123",
		MCPConfigPath:   "/tmp/mcp.json",
		Plugins:         []PluginRef{{Name: "p1", Path: "/plugins/p1"}},
		ExtraArgs:       []string{"--debug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	argv := spec.Argv
	assertContainsPair(t, argv, "--model", "claude-opus-4-8")
	assertContainsPair(t, argv, "--append-system-prompt", "be terse")
	assertContainsPair(t, argv, "--resume", "sess-123")
	assertContainsPair(t, argv, "--mcp-config", "/tmp/mcp.json")
	assertContainsPair(t, argv, "--plugin-dir", "/plugins/p1")
	if !slices.Contains(argv, "--continue") {
		t.Fatalf("argv missing --continue: %v", argv)
	}
	// ExtraArgs come last.
	if argv[len(argv)-1] != "--debug" {
		t.Fatalf("argv last = %q, want --debug: %v", argv[len(argv)-1], argv)
	}
}

func TestClaudeBuildCommandModelViaEnv(t *testing.T) {
	s := NewClaude(WithModelEnv("ANTHROPIC_MODEL")).NewSession()
	spec, err := s.BuildCommand(context.Background(), Request{Prompt: "hi", Model: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(spec.Argv, "--model") {
		t.Fatalf("model should be in env, not argv: %v", spec.Argv)
	}
	if !slices.Contains(spec.Env, "ANTHROPIC_MODEL=m1") {
		t.Fatalf("env missing ANTHROPIC_MODEL=m1: %v", spec.Env)
	}
}

func TestClaudeBuildCommandEnvMerge(t *testing.T) {
	s := NewClaude(WithBaseEnv(map[string]string{"BASE": "1"})).NewSession()
	spec, err := s.BuildCommand(context.Background(), Request{
		Prompt: "hi",
		Env:    map[string]string{"REQ": "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.Env, "BASE=1") || !slices.Contains(spec.Env, "REQ=2") {
		t.Fatalf("env merge failed: %v", spec.Env)
	}
	// Env must be sorted for determinism.
	if !slices.IsSorted(spec.Env) {
		t.Fatalf("env not sorted: %v", spec.Env)
	}
}

func TestClaudeBuildCommandCustomBinary(t *testing.T) {
	s := NewClaude(WithBinary("/usr/local/bin/claude")).NewSession()
	spec, _ := s.BuildCommand(context.Background(), Request{Prompt: "x"})
	if spec.Argv[0] != "/usr/local/bin/claude" {
		t.Fatalf("argv[0] = %q, want custom binary", spec.Argv[0])
	}
}

func TestClaudeParseAssistantText(t *testing.T) {
	s := NewClaude().NewSession()
	line := `{"type":"assistant","session_id":"s1","message":{"role":"assistant","content":[{"type":"text","text":"Hello there"}]}}` + "\n"
	events, err := s.ParseChunk([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	got := findEvent(events, EventAgentMessage)
	if got == nil {
		t.Fatalf("no agent.message event in %v", events)
	}
	if got.Payload["text"] != "Hello there" {
		t.Fatalf("text = %v, want 'Hello there'", got.Payload["text"])
	}
}

func TestClaudeParseToolUse(t *testing.T) {
	s := NewClaude().NewSession()
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}` + "\n"
	events, _ := s.ParseChunk([]byte(line))
	got := findEvent(events, EventToolCall)
	if got == nil {
		t.Fatalf("no agent.tool_call in %v", events)
	}
	if got.Payload["name"] != "Bash" {
		t.Fatalf("name = %v, want Bash", got.Payload["name"])
	}
	if got.Payload["id"] != "t1" {
		t.Fatalf("id = %v, want t1", got.Payload["id"])
	}
}

func TestClaudeParseToolResult(t *testing.T) {
	s := NewClaude().NewSession()
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file.txt"}]}}` + "\n"
	events, _ := s.ParseChunk([]byte(line))
	got := findEvent(events, EventToolResult)
	if got == nil {
		t.Fatalf("no agent.tool_result in %v", events)
	}
	if got.Payload["tool_use_id"] != "t1" {
		t.Fatalf("tool_use_id = %v, want t1", got.Payload["tool_use_id"])
	}
}

func TestClaudeSniffsSessionID(t *testing.T) {
	s := NewClaude().NewSession()
	line := `{"type":"system","subtype":"init","session_id":"abc-999"}` + "\n"
	if _, err := s.ParseChunk([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if s.SessionID() != "abc-999" {
		t.Fatalf("SessionID = %q, want abc-999", s.SessionID())
	}
}

func TestClaudeRateLimitEvent(t *testing.T) {
	s := NewClaude().NewSession()
	line := `{"type":"rate_limit_event","retry_after":30}` + "\n"
	events, _ := s.ParseChunk([]byte(line))
	if findEvent(events, EventRateLimit) == nil {
		t.Fatalf("no provider.rate_limit in %v", events)
	}
}

func TestClaudeUsageAccumulatesAndResultOverrides(t *testing.T) {
	s := NewClaude().NewSession()
	// Two assistant turns accumulate usage.
	s.ParseChunk([]byte(`{"type":"assistant","message":{"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"))
	s.ParseChunk([]byte(`{"type":"assistant","message":{"usage":{"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":7}}}` + "\n"))
	// Result frame sets authoritative totals + cost.
	res, _, err := s.Finalize(context.Background(),
		[]byte(`{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":13,"output_tokens":7,"cache_read_input_tokens":7},"total_cost_usd":0.0123}`+"\n"),
		0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "done" {
		t.Fatalf("summary = %q, want done", res.Summary)
	}
	if res.Usage.InputTokens != 13 || res.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v, want input 13 output 7", res.Usage)
	}
	if res.Usage.CacheTokens != 7 {
		t.Fatalf("cache = %d, want 7", res.Usage.CacheTokens)
	}
	if res.Usage.EstimatedCostUSD != 0.0123 {
		t.Fatalf("cost = %v, want 0.0123", res.Usage.EstimatedCostUSD)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0", res.ExitCode)
	}
}

// --- helpers ---

func findEvent(events []Event, typ string) *Event {
	for i := range events {
		if events[i].Type == typ {
			return &events[i]
		}
	}
	return nil
}

func assertContainsPair(t *testing.T, argv []string, flag, val string) {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return
		}
	}
	t.Fatalf("argv missing %q %q: %v", flag, val, argv)
}
