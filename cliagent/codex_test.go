package cliagent

import (
	"context"
	"slices"
	"testing"
)

func TestCodexProviderMeta(t *testing.T) {
	p := NewCodex()
	if p.Name() != "codex" {
		t.Fatalf("Name = %q, want codex", p.Name())
	}
	caps := p.Capabilities()
	if !caps.Streaming || !caps.Resume {
		t.Fatalf("codex should stream + resume, got %+v", caps)
	}
	if caps.Plugins {
		t.Fatalf("codex has no plugin support, got %+v", caps)
	}
}

func TestCodexBuildCommandBasic(t *testing.T) {
	s := NewCodex().NewSession()
	spec, err := s.BuildCommand(context.Background(), Request{Prompt: "hello", WorkspacePath: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "--json", "hello"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("Argv = %v, want %v", spec.Argv, want)
	}
	if spec.WorkDir != "/w" {
		t.Fatalf("WorkDir = %q, want /w", spec.WorkDir)
	}
}

func TestCodexBuildCommandSystemPromptPrepended(t *testing.T) {
	s := NewCodex().NewSession()
	spec, _ := s.BuildCommand(context.Background(), Request{
		Prompt:       "do it",
		SystemPrompt: "you are careful",
	})
	last := spec.Argv[len(spec.Argv)-1]
	want := "you are careful\n\ndo it"
	if last != want {
		t.Fatalf("prompt arg = %q, want %q", last, want)
	}
}

func TestCodexBuildCommandModelAndResume(t *testing.T) {
	s := NewCodex().NewSession()
	spec, _ := s.BuildCommand(context.Background(), Request{
		Prompt:          "hi",
		Model:           "gpt-x",
		ResumeSessionID: "thread-7",
		ExtraArgs:       []string{"--full-auto"},
	})
	assertContainsPair(t, spec.Argv, "--model", "gpt-x")
	// resume subcommand carries the thread id
	if !slices.Contains(spec.Argv, "resume") || !slices.Contains(spec.Argv, "thread-7") {
		t.Fatalf("argv missing resume thread-7: %v", spec.Argv)
	}
	if !slices.Contains(spec.Argv, "--full-auto") {
		t.Fatalf("argv missing extra arg: %v", spec.Argv)
	}
}

func TestCodexParseAssistantMessage(t *testing.T) {
	s := NewCodex().NewSession()
	line := `{"type":"item.completed","item":{"type":"assistant_message","text":"All set"}}` + "\n"
	events, _ := s.ParseChunk([]byte(line))
	got := findEvent(events, EventAgentMessage)
	if got == nil || got.Payload["text"] != "All set" {
		t.Fatalf("want agent.message 'All set', got %v", events)
	}
}

func TestCodexParseCommandExecution(t *testing.T) {
	s := NewCodex().NewSession()
	line := `{"type":"item.completed","item":{"type":"command_execution","command":"ls -la"}}` + "\n"
	events, _ := s.ParseChunk([]byte(line))
	got := findEvent(events, EventToolCall)
	if got == nil {
		t.Fatalf("want agent.tool_call, got %v", events)
	}
	if got.Payload["command"] != "ls -la" {
		t.Fatalf("command = %v, want 'ls -la'", got.Payload["command"])
	}
}

func TestCodexSniffsThreadID(t *testing.T) {
	s := NewCodex().NewSession()
	s.ParseChunk([]byte(`{"type":"thread.started","thread_id":"th-42"}` + "\n"))
	if s.SessionID() != "th-42" {
		t.Fatalf("SessionID = %q, want th-42", s.SessionID())
	}
}

func TestCodexCapturesUsageOnTurnCompleted(t *testing.T) {
	s := NewCodex().NewSession()
	res, _, err := s.Finalize(context.Background(),
		[]byte(`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":40,"cached_input_tokens":12}}`+"\n"),
		0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 40 || res.Usage.CacheTokens != 12 {
		t.Fatalf("usage = %+v, want 100/40/12", res.Usage)
	}
}

func TestCodexSummaryFromLastAssistantMessage(t *testing.T) {
	s := NewCodex().NewSession()
	s.ParseChunk([]byte(`{"type":"item.completed","item":{"type":"assistant_message","text":"first"}}` + "\n"))
	res, _, _ := s.Finalize(context.Background(),
		[]byte(`{"type":"item.completed","item":{"type":"assistant_message","text":"final answer"}}`+"\n"), 0)
	if res.Summary != "final answer" {
		t.Fatalf("summary = %q, want 'final answer'", res.Summary)
	}
}
