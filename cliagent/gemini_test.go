package cliagent

import (
	"context"
	"slices"
	"testing"
)

func TestGeminiProviderMeta(t *testing.T) {
	p := NewGemini()
	if p.Name() != "gemini" {
		t.Fatalf("Name = %q, want gemini", p.Name())
	}
	if !p.Capabilities().Streaming {
		t.Fatalf("gemini should stream, got %+v", p.Capabilities())
	}
}

func TestGeminiBuildCommandBasic(t *testing.T) {
	s := NewGemini().NewSession()
	spec, err := s.BuildCommand(context.Background(), Request{Prompt: "hi there", WorkspacePath: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gemini", "--output-format", "json"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("Argv = %v, want %v", spec.Argv, want)
	}
	if string(spec.Stdin) != "hi there" {
		t.Fatalf("Stdin = %q, want 'hi there'", spec.Stdin)
	}
}

func TestGeminiBuildCommandModelAndSystemPrompt(t *testing.T) {
	s := NewGemini().NewSession()
	spec, _ := s.BuildCommand(context.Background(), Request{
		Prompt:       "go",
		SystemPrompt: "be brief",
		Model:        "gemini-2.5-pro",
	})
	assertContainsPair(t, spec.Argv, "-m", "gemini-2.5-pro")
	if string(spec.Stdin) != "be brief\n\ngo" {
		t.Fatalf("Stdin = %q, want prepended system prompt", spec.Stdin)
	}
}

func TestGeminiFinalizeParsesResponseAndUsage(t *testing.T) {
	s := NewGemini().NewSession()
	out := `{"response":"the answer","stats":{"models":{"gemini-2.5-pro":{"tokens":{"prompt":50,"candidates":20,"cached":5}}}}}` + "\n"
	res, events, err := s.Finalize(context.Background(), []byte(out), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "the answer" {
		t.Fatalf("summary = %q, want 'the answer'", res.Summary)
	}
	if res.Usage.InputTokens != 50 || res.Usage.OutputTokens != 20 || res.Usage.CacheTokens != 5 {
		t.Fatalf("usage = %+v, want 50/20/5", res.Usage)
	}
	if findEvent(events, EventAgentMessage) == nil {
		t.Fatalf("want agent.message event, got %v", events)
	}
}

func TestGeminiUsageSumsAcrossModels(t *testing.T) {
	s := NewGemini().NewSession()
	out := `{"response":"x","stats":{"models":{"a":{"tokens":{"prompt":10,"candidates":1}},"b":{"tokens":{"prompt":5,"candidates":2}}}}}`
	res, _, _ := s.Finalize(context.Background(), []byte(out), 0)
	if res.Usage.InputTokens != 15 || res.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v, want input 15 output 3", res.Usage)
	}
}
