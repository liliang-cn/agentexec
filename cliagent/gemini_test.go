package cliagent

import (
	"context"
	"slices"
	"testing"
)

func newGemini() Session {
	return NewGemini(WithName("gemini-cli"), WithAllowedModes([]string{"headless-code", "terminal-task"})).NewSession()
}

func TestGeminiMeta(t *testing.T) {
	if NewGemini(WithName("gemini-cli")).Name() != "gemini-cli" {
		t.Fatal("name")
	}
}

func TestGeminiGoldenArgv(t *testing.T) {
	spec, err := newGemini().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "do it", PermissionMode: PermissionBypass, Sandbox: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gemini", "--prompt", "do it", "--output-format", "stream-json", "--yolo", "--skip-trust"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=\n%v\nwant\n%v", spec.Argv, want)
	}
}

func TestGeminiInitAndResult(t *testing.T) {
	s := newGemini()
	ev, _ := s.ParseChunk([]byte(`{"type":"init","session_id":"x","model":"auto-gemini-3"}` + "\n"))
	if m := findEvent(ev, EventAgentMessage); m == nil || m.Payload["role"] != "system" || m.Payload["raw"] == nil {
		t.Fatalf("init=%v", ev)
	}
	s.ParseChunk([]byte(`{"type":"result","status":"success","stats":{"input_tokens":50,"output_tokens":20,"cached":5}}` + "\n"))
	res, _, _ := s.Finalize(context.Background(), nil, 0)
	if res.Usage.Model != "auto-gemini-3" || res.Usage.InputTokens != 50 || res.Usage.OutputTokens != 20 || res.Usage.CacheTokens != 5 {
		t.Fatalf("usage=%+v", res.Usage)
	}
}

func TestGeminiAssistantDelta(t *testing.T) {
	s := newGemini()
	ev, _ := s.ParseChunk([]byte(`{"type":"message","role":"assistant","content":"partial","delta":true}` + "\n"))
	m := findEvent(ev, EventAgentMessage)
	if m == nil || m.Payload["text"] != "partial" || m.Payload["delta"] != true {
		t.Fatalf("assistant=%v", m)
	}
}
