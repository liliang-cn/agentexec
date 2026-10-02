package agentexec

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

// Recorded from codex-cli 0.149.0 against a model that needs a newer CLI. The
// `error` frame alone is not a verdict (Codex sends warnings as `error` too);
// turn.failed is.
func TestCodexTurnFailedIsAVerdict(t *testing.T) {
	out := `{"type":"thread.started","thread_id":"t-1"}
{"type":"turn.started"}
{"type":"error","message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"The 'gpt-6-astra' model requires a newer version of Codex. Please upgrade to the latest app or CLI and try again.\"}}"}
{"type":"turn.failed","error":{"message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"The 'gpt-6-astra' model requires a newer version of Codex. Please upgrade to the latest app or CLI and try again.\"}}"}}
`
	res, _, err := NewCodex().NewSession().Finalize(context.Background(), []byte(out), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed {
		t.Fatalf("turn.failed was not reported as a failure: %+v", res)
	}
	want := "The 'gpt-6-astra' model requires a newer version of Codex. Please upgrade to the latest app or CLI and try again."
	if res.Summary != want {
		t.Fatalf("summary=%q\nwant=%q", res.Summary, want)
	}
}

// A warning-shaped error frame on a turn that then completes must stay a
// success; this is why `error` was never treated as a verdict.
func TestCodexAWarningErrorFrameIsNotAFailure(t *testing.T) {
	out := `{"type":"error","message":"Skill descriptions were shortened to fit the skills context budget."}
{"type":"item.completed","item":{"type":"agent_message","text":"pong"}}
{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":1}}
`
	res, _, err := NewCodex().NewSession().Finalize(context.Background(), []byte(out), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed || res.Summary != "pong" {
		t.Fatalf("res=%+v", res)
	}
}
