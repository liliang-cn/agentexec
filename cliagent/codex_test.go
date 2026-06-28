package cliagent

import (
	"context"
	"slices"
	"testing"
)

func newCodex() Session {
	return NewCodex(WithName("codex"), WithAllowedModes([]string{"headless-code", "terminal-task"})).NewSession()
}

func TestCodexMeta(t *testing.T) {
	p := NewCodex(WithName("codex"))
	if p.Name() != "codex" || !p.Capabilities().Streaming {
		t.Fatalf("meta wrong")
	}
}

func TestCodexGoldenArgv(t *testing.T) {
	spec, err := newCodex().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "do it", WorkspacePath: "/w",
		PermissionMode: PermissionBypass, Sandbox: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--", "do it"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=\n%v\nwant\n%v", spec.Argv, want)
	}
}

func TestCodexResumeArgv(t *testing.T) {
	spec, _ := newCodex().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "p", ResumeSessionID: "th7", Sandbox: false,
	})
	want := []string{"codex", "exec", "resume", "th7", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--", "p"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%v", spec.Argv)
	}
}

func TestCodexSandboxedOmitsBypass(t *testing.T) {
	spec, _ := newCodex().BuildCommand(context.Background(), Request{Mode: "headless-code", Prompt: "p", Sandbox: true})
	if slices.Contains(spec.Argv, "--dangerously-bypass-approvals-and-sandbox") || slices.Contains(spec.Argv, "--skip-git-repo-check") {
		t.Fatalf("sandboxed run should omit bypass flags: %v", spec.Argv)
	}
}

func TestCodexSystemPromptPrepended(t *testing.T) {
	spec, _ := newCodex().BuildCommand(context.Background(), Request{Mode: "headless-code", Prompt: "go", SystemPrompt: "be careful", Sandbox: true})
	if spec.Argv[len(spec.Argv)-1] != "be careful\n\ngo" {
		t.Fatalf("prompt=%q", spec.Argv[len(spec.Argv)-1])
	}
}

func TestCodexAssistantAndUsage(t *testing.T) {
	s := newCodex()
	s.ParseChunk([]byte(`{"type":"thread.started","thread_id":"th9"}` + "\n"))
	ev, _ := s.ParseChunk([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"All set"}}` + "\n"))
	if m := findEvent(ev, EventAgentMessage); m == nil || m.Payload["text"] != "All set" {
		t.Fatalf("msg=%v", ev)
	}
	s.ParseChunk([]byte(`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":40,"cached_input_tokens":12,"reasoning_output_tokens":8}}` + "\n"))
	res, _, _ := s.Finalize(context.Background(), nil, 0)
	if res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 48 || res.Usage.CacheTokens != 12 {
		t.Fatalf("usage=%+v", res.Usage)
	}
	if s.SessionID() != "th9" {
		t.Fatalf("sid=%q", s.SessionID())
	}
}

func TestCodexCommandExecutionIsToolCall(t *testing.T) {
	s := newCodex()
	ev, _ := s.ParseChunk([]byte(`{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}` + "\n"))
	if findEvent(ev, EventToolCall) == nil {
		t.Fatalf("no tool_call: %v", ev)
	}
}
