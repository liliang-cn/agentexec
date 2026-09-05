package agentexec

import (
	"context"
	"slices"
	"testing"
)

func TestCursorMeta(t *testing.T) {
	p := NewCursor(WithName("cursor-work"))
	if p.Name() != "cursor-work" {
		t.Fatalf("name=%q", p.Name())
	}
	c := p.Capabilities()
	if !c.Streaming || !c.Resume || !c.SupportsPTY || !c.RequiresWorkspace {
		t.Fatalf("caps=%+v", c)
	}
	// Claiming either would invite a caller to pass plugin dirs or MCP config
	// that BuildCommand silently drops.
	if c.Plugins || c.MCP {
		t.Fatalf("cursor must not claim plugins or MCP: %+v", c)
	}
}

func TestCursorGoldenArgv(t *testing.T) {
	spec, err := NewCursor(WithModelEnv("CURSOR_MODEL")).NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work",
		PermissionMode:  PermissionBypass,
		ResumeSessionID: "chat-7",
		SystemPrompt:    "be brief",
		Env:             map[string]string{"CURSOR_MODEL": "sonnet"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every flag differs from claude's: no --verbose, bypass is --force, and
	// the headless posture is --trust. The system prompt has no flag of its own
	// and rides in front of the prompt, which stays last and un-dashed.
	want := []string{
		"cursor-agent", "--print", "--output-format", "stream-json",
		"--model", "sonnet", "--resume", "chat-7", "--force",
		"--trust", "--sandbox", "disabled",
		"be brief\n\ndo it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
	if spec.WorkDir != "/work" {
		t.Fatalf("workdir=%q", spec.WorkDir)
	}
	if !slices.Contains(spec.Env, "CURSOR_MODEL=sonnet") {
		t.Fatalf("env=%v", spec.Env)
	}
}

func TestCursorSandboxTrueKeepsTheTrustPrompt(t *testing.T) {
	spec, err := NewCursor().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work", Sandbox: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(spec.Argv, "--trust") {
		t.Fatalf("Sandbox: true must run inside cursor's own trust flow, got %q", spec.Argv)
	}
}

func TestCursorSessionParsesTheClaudeDialect(t *testing.T) {
	session := NewCursor().NewSession()

	// The point of borrowing claude's session: cursor-agent's stream-json is
	// the same dialect, so the answer is the assistant-role message and the
	// is_error verdict survives an exit code of zero.
	frames := `{"type":"system","subtype":"init","session_id":"chat-7"}
{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]},"session_id":"chat-7"}
{"type":"result","subtype":"success","is_error":true,"result":"quota exceeded","session_id":"chat-7"}
`
	events, err := session.ParseChunk([]byte(frames))
	if err != nil {
		t.Fatalf("ParseChunk: %v", err)
	}
	var sawAssistant bool
	for _, e := range events {
		if e.Type == EventAgentMessage && e.Payload["role"] == "assistant" {
			sawAssistant = true
		}
	}
	if !sawAssistant {
		t.Errorf("expected an assistant message among %+v", events)
	}
	res, _, err := session.Finalize(context.Background(), nil, 0)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if !res.Failed {
		t.Error("is_error true must be a failure even with exit code 0")
	}
	if session.SessionID() != "chat-7" {
		t.Errorf("session id = %q, want chat-7", session.SessionID())
	}
}
