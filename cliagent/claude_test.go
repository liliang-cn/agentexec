package cliagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	if !strings.Contains(string(data), `"aas"`) || !strings.Contains(string(data), `"p"`) {
		t.Fatalf("merged config = %s", data)
	}
}

func TestClaudeParseUserToolResult(t *testing.T) {
	s := newClaudeAAS()
	ev, _ := s.ParseChunk([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}` + "\n"))
	r := findEvent(ev, EventToolResult)
	if r == nil {
		t.Fatalf("no tool_result event: %v", ev)
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
	s.ParseChunk([]byte(`{"type":"result","result":"done","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2},"total_cost_usd":0.05}` + "\n"))
	res, _, _ := s.Finalize(context.Background(), nil, 0)
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 4 || res.Usage.CacheTokens != 2 || res.Usage.EstimatedCostUSD != 0.05 {
		t.Fatalf("usage=%+v", res.Usage)
	}
	if res.Summary != "done" {
		t.Fatalf("summary=%q", res.Summary)
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

// An empty ExtraMCPServers map cannot say "no MCP servers": no servers means no
// --mcp-config flag, which means the CLI loads every server the developer has
// configured. NoMCP is the way to say it.
func TestClaudeNoMCPWritesAnEmptyConfigAndGoesStrict(t *testing.T) {
	ws := t.TempDir()
	spec, err := NewClaude().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "p", WorkspacePath: ws, NoMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(spec.Argv, "--mcp-config")
	if i < 0 {
		t.Fatalf("no --mcp-config: %v", spec.Argv)
	}
	if !slices.Contains(spec.Argv, "--strict-mcp-config") {
		t.Fatalf("NoMCP without the strict flag still merges the user's servers: %v", spec.Argv)
	}
	// --mcp-config is variadic: a value in the last position before the prompt
	// swallows the prompt as a second config path.
	if spec.Argv[i+1] == spec.Argv[len(spec.Argv)-1] {
		t.Errorf("the config path is immediately before the prompt: %v", spec.Argv)
	}
	body, err := os.ReadFile(filepath.Join(ws, ".mcp-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"mcpServers"`) || strings.Contains(string(body), "command") {
		t.Errorf("config = %s, want an empty server set", body)
	}
}

// Explicit servers win: asking for both none and some is a caller bug, and the
// named servers are the clearer intent.
func TestClaudeNoMCPYieldsToExplicitServers(t *testing.T) {
	ws := t.TempDir()
	spec, _ := NewClaude().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "p", WorkspacePath: ws, NoMCP: true,
		ExtraMCPServers: map[string]any{"x": map[string]any{"command": "exe"}},
	})
	body, err := os.ReadFile(filepath.Join(ws, ".mcp-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"x"`) {
		t.Errorf("config = %s, want the explicit server", body)
	}
	_ = spec
}

// Finalize takes a fullOutput parameter. It used to discard it, so a caller
// that collected the output and passed the whole thing got an empty Result and
// no error — the least debuggable outcome available.
func TestClaudeFinalizeParsesOutputWhenNothingWasStreamed(t *testing.T) {
	out := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"OK"}]}}` + "\n" +
		`{"type":"result","result":"OK","usage":{"input_tokens":1,"output_tokens":2}}` + "\n")
	res, events, err := NewClaude().NewSession().Finalize(context.Background(), out, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "OK" {
		t.Errorf("summary = %q, want OK", res.Summary)
	}
	var texts []string
	for _, e := range events {
		if e.Type == EventAgentMessage {
			if s, _ := e.Payload["text"].(string); s != "" {
				texts = append(texts, s)
			}
		}
	}
	if len(texts) != 1 || texts[0] != "OK" {
		t.Errorf("texts = %v, want [OK]", texts)
	}
}

// A caller that streamed must not get the output parsed twice.
func TestClaudeFinalizeDoesNotDoubleParseAfterStreaming(t *testing.T) {
	out := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"OK"}]}}` + "\n")
	s := NewClaude().NewSession()
	streamed, _ := s.ParseChunk(out)
	_, tail, _ := s.Finalize(context.Background(), out, 0)
	if len(streamed) != 1 {
		t.Fatalf("streamed = %d events", len(streamed))
	}
	if len(tail) != 0 {
		t.Errorf("Finalize re-parsed %d event(s) the caller already had", len(tail))
	}
}

// A revoked OAuth token makes claude write "Failed to authenticate" as an
// assistant message, set is_error on the result frame, and exit zero. A caller
// reading only the message and the exit code takes that for the model's answer.
func TestClaudeResultCarriesIsError(t *testing.T) {
	out := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Failed to authenticate. API Error: 401"}]}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":true,"result":"Failed to authenticate. API Error: 401"}` + "\n")
	res, _, err := NewClaude().NewSession().Finalize(context.Background(), out, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed {
		t.Error("is_error was not carried into Result.Failed")
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d; the point is that it is zero", res.ExitCode)
	}
}

func TestClaudeHealthyResultIsNotFailed(t *testing.T) {
	out := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"OK"}` + "\n")
	res, _, _ := NewClaude().NewSession().Finalize(context.Background(), out, 0)
	if res.Failed {
		t.Error("a healthy turn was marked failed")
	}
}
